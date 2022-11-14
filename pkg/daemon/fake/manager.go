//go:build windows

package fake

import (
	"fmt"
	"sync"

	"github.com/pkg/errors"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"

	"github.com/openshift/windows-machine-config-operator/pkg/daemon/winsvc"
)

// fakeServiceList mocks out the state of all services on a Windows instance
type fakeServiceList struct {
	m    *sync.Mutex
	svcs map[string]winsvc.Service
}

// write overwrites the given service to the svcs map
func (l *fakeServiceList) write(name string, svc winsvc.Service) {
	l.m.Lock()
	defer l.m.Unlock()
	l.svcs[name] = svc
}

// read returns the entry with the given name, and a bool indicating if it exists or not
func (l *fakeServiceList) read(name string) (winsvc.Service, bool) {
	l.m.Lock()
	defer l.m.Unlock()
	service, exists := l.svcs[name]
	return service, exists
}

// listServiceNames returns a slice of all service names
func (l *fakeServiceList) listServiceNames() []string {
	l.m.Lock()
	defer l.m.Unlock()
	var names []string
	for svcName := range l.svcs {
		names = append(names, svcName)
	}
	return names
}

// remove deletes the entry with the given name, throwing an error if it doesn't exist
func (l *fakeServiceList) remove(name string) error {
	l.m.Lock()
	defer l.m.Unlock()
	_, exists := l.svcs[name]
	if !exists {
		return errors.New("service does not exist")
	}
	delete(l.svcs, name)
	return nil
}

func newFakeServiceList() *fakeServiceList {
	return &fakeServiceList{
		m:    &sync.Mutex{},
		svcs: make(map[string]winsvc.Service),
	}
}

type testMgr struct {
	svcList *fakeServiceList
}

// CreateService installs new service name on the system.
// The service will be executed by running exepath binary.
// Use config c to specify service parameters.
// Any args will be passed as command-line arguments when
// the service is started; these arguments are distinct from
// the arguments passed to Service.Start or via the "Start
// parameters" field in the service's Properties dialog box.
func (t *testMgr) CreateService(name, exepath string, config mgr.Config, args ...string) (winsvc.Service, error) {
	// Throw an error if the service already exists
	if _, ok := t.svcList.read(name); ok {
		return nil, errors.New("service already exists")
	}
	config.BinaryPathName = exepath
	service := FakeService{
		name:   name,
		config: config,
		status: svc.Status{
			State: svc.Stopped,
		},
		serviceList: t.svcList,
	}
	t.svcList.write(name, &service)
	return &service, nil
}

func (t *testMgr) GetServices() (map[string]struct{}, error) {
	svcsList := t.svcList.listServiceNames()
	svcsMap := make(map[string]struct{})
	for _, svc := range svcsList {
		svcsMap[svc] = struct{}{}
	}
	return svcsMap, nil
}

func (t *testMgr) OpenService(name string) (winsvc.Service, error) {
	service, exists := t.svcList.read(name)
	if !exists {
		return nil, fmt.Errorf("service does not exist")
	}
	return service, nil
}

func (t *testMgr) DeleteService(name string) error {
	winSvc, exists := t.svcList.read(name)
	if !exists {
		// Nothing to do if it already does not exist
		return nil
	}
	// Ensure service is stopped before deleting
	if err := t.EnsureServiceState(winSvc, svc.Stopped); err != nil {
		return errors.Wrapf(err, "failed to stop service %q", name)
	}
	return t.svcList.remove(name)
}

func (t *testMgr) EnsureServiceState(service winsvc.Service, state svc.State) error {
	status, err := service.Query()
	if err != nil {
		return errors.Wrap(err, "error querying service state")
	}
	if status.State == state {
		return nil
	}
	switch state {
	case svc.Running:
		return service.Start()
	case svc.Stopped:
		fakeService, ok := service.(*FakeService)
		if !ok {
			return fmt.Errorf("service is not correct type")
		}
		dependentServices, err := t.listDependentServices(fakeService.name)
		if err != nil {
			return err
		}
		for _, dependentServiceName := range dependentServices {
			dependentSvc, err := t.OpenService(dependentServiceName)
			if err != nil {
				return fmt.Errorf("error opening dependent service %s", dependentServiceName)
			}
			err = t.EnsureServiceState(dependentSvc, svc.Stopped)
			if err != nil {
				return errors.Wrapf(err, "unable to stop dependent service %s", dependentServiceName)
			}
		}

		_, err = service.Control(svc.Stop)
		return err
	default:
		return fmt.Errorf("unexpected state request: %d", state)
	}
}

func (t *testMgr) listDependentServices(serviceName string) ([]string, error) {
	var dependencies []string
	for name, svc := range t.svcList.svcs {
		if name == serviceName {
			continue
		}
		config, err := svc.Config()
		if err != nil {
			return nil, err
		}
		for _, s := range config.Dependencies {
			if s == serviceName {
				dependencies = append(dependencies, name)
			}
		}
	}
	return dependencies, nil
}

func NewTestMgr(existingServices map[string]*FakeService) *testMgr {
	testMgr := &testMgr{newFakeServiceList()}
	if existingServices != nil {
		for name, svc := range existingServices {
			svc.serviceList = testMgr.svcList
			testMgr.svcList.svcs[name] = svc
		}
	}
	return testMgr
}
