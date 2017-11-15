/*
Copyright 2017 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"fmt"
	"io/ioutil"
	"net"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/golang/glog"
	"golang.org/x/net/context"
	"google.golang.org/grpc"

	pluginapi "k8s.io/kubernetes/pkg/kubelet/apis/deviceplugin/v1alpha"
	utilfs "k8s.io/kubernetes/pkg/util/filesystem"
)

const (
	dummyDeviceRE string = "^device-[0-9]*$"
)

// StubDevicePlugin contains grpc info and devices info
type StubDevicePlugin struct {
	// basic info
	resourceName string

	// grpc info
	endpoint   string
	grpcServer *grpc.Server

	// devices of this DevicePlugin
	mutex   sync.Mutex
	devices map[string]pluginapi.Device

	// dummy device file watcher
	watcher     utilfs.FSWatcher
	watcherPath string

	// internal chan
	updateChan chan bool
}

func getDevices(dir string) map[string]pluginapi.Device {
	files, err := ioutil.ReadDir(dir)
	if err != nil {
		return nil
	}

	devices := make(map[string]pluginapi.Device)
	reg := regexp.MustCompile(dummyDeviceRE)

	// read from tmp dir
	for _, f := range files {
		if f.IsDir() {
			continue
		}

		// match dummyDeviceRE
		if reg.MatchString(f.Name()) {
			glog.Infof("found dummy device: %s", f.Name())

			// we use file name as device id
			id := f.Name()

			// read device state from file
			state, err := ioutil.ReadFile(filepath.Join(dir, f.Name()))
			if err != nil {
				continue
			}

			devices[f.Name()] = pluginapi.Device{
				ID:     id,
				Health: strings.Replace(string(state), "\n", "", -1),
			}
		}
	}

	glog.Infof("getDevices: %v", devices)
	return devices
}

// NewStubDevicePlugin returns an initialized StubDevicePlugin.
func NewStubDevicePlugin(endpoint, dir, resourceName string) (*StubDevicePlugin, error) {
	devices := getDevices(dir)
	if len(devices) == 0 {
		return nil, fmt.Errorf("No devices found in dir: %s", dir)
	}

	glog.Infof("NewStubDevicePlugin, endpoint: %s, dir: %s", endpoint, dir)
	return &StubDevicePlugin{
		resourceName: resourceName,
		endpoint:     endpoint + fmt.Sprintf("-%d", time.Now().Unix()),
		devices:      devices,
		watcher:      utilfs.NewFsnotifyWatcher(),
		watcherPath:  dir,
		updateChan:   make(chan bool),
	}, nil
}

// Register registers the device plugin to Kubelet.
func (s *StubDevicePlugin) Register(kubeletEndpoint string) error {
	conn, err := grpc.Dial(kubeletEndpoint, grpc.WithInsecure(), grpc.WithBlock(),
		grpc.WithDialer(func(addr string, timeout time.Duration) (net.Conn, error) {
			return net.DialTimeout("unix", addr, timeout)
		}))
	defer conn.Close()
	if err != nil {
		return err
	}
	client := pluginapi.NewRegistrationClient(conn)
	reqt := &pluginapi.RegisterRequest{
		Version:      pluginapi.Version,
		Endpoint:     path.Base(s.endpoint),
		ResourceName: s.resourceName,
	}

	_, err = client.Register(context.Background(), reqt)
	if err != nil {
		return fmt.Errorf("Cannot register stub worker to kubelet service: %v", err)
	}
	return nil
}

// ListAndWatch lists devices and update that list according to the Update call
func (s *StubDevicePlugin) ListAndWatch(emtpy *pluginapi.Empty, server pluginapi.DevicePlugin_ListAndWatchServer) error {
	if err := s.sendDevicesToKubelet(server); err != nil {
		glog.Errorf("device-plugin: cannot update device states: %v", err)
		return err
	}

	for {
		select {
		case needUpdate := <-s.updateChan:
			if !needUpdate {
				continue
			}

			if err := s.sendDevicesToKubelet(server); err != nil {
				glog.Errorf("device-plugin: cannot update device states: %v", err)
				return err
			}
		}
	}

}

func (s *StubDevicePlugin) sendDevicesToKubelet(server pluginapi.DevicePlugin_ListAndWatchServer) error {
	resp := new(pluginapi.ListAndWatchResponse)
	for _, dev := range s.devices {
		resp.Devices = append(resp.Devices, &pluginapi.Device{
			ID:     dev.ID,
			Health: dev.Health,
		})
	}

	if err := server.Send(resp); err != nil {
		return fmt.Errorf("device-plugin: cannot update device states: %v", err)
	}

	glog.Infof("send devices: %v", resp)
	return nil
}

// Allocate which return list of devices.
func (s *StubDevicePlugin) Allocate(ctx context.Context, r *pluginapi.AllocateRequest) (*pluginapi.AllocateResponse, error) {
	var response pluginapi.AllocateResponse

	for _, id := range r.DevicesIDs {
		dev, exists := s.devices[id]
		if !exists {
			return nil, fmt.Errorf("Invalid allocation request with non-existing device %s", id)
		}
		if dev.Health != pluginapi.Healthy {
			return nil, fmt.Errorf("Invalid allocation request with unhealthy device %s", id)
		}
		response.Devices = append(response.Devices, &pluginapi.DeviceSpec{
			HostPath:      path.Join(s.watcherPath, dev.ID),
			ContainerPath: path.Join(s.watcherPath, dev.ID),
			Permissions:   "mrw",
		})

		response.Mounts = append(response.Mounts, &pluginapi.Mount{
			ContainerPath: path.Join(s.watcherPath, dev.ID),
			HostPath:      path.Join(s.watcherPath, dev.ID),
		})
	}

	glog.Infof("Allocate response: %v", response)
	return &response, nil
}

// Start will start grpc server and register to kubelet
func (s *StubDevicePlugin) Start() error {
	// Start grpc server
	glog.Infof("StubDevicePlugin start at: %s", s.endpoint)
	err := s.cleanup()
	if err != nil {
		return err
	}

	socket, err := net.Listen("unix", s.endpoint)
	if err != nil {
		return err
	}

	s.grpcServer = grpc.NewServer()
	pluginapi.RegisterDevicePluginServer(s.grpcServer, s)

	go s.grpcServer.Serve(socket)

	// Wait till the grpcServer is ready to serve services.
	for {
		if s.grpcServer != nil {
			services := s.grpcServer.GetServiceInfo()
			if len(services) > 0 {
				break
			}
		}

		time.Sleep(1 * time.Second)
	}

	// Register to kubelet
	err = s.Register(pluginapi.KubeletSocket)
	if err != nil {
		glog.Infof("Could not register device plugin: %s", err)
		s.Stop()
		return err
	}

	glog.Info("Registered device plugin with Kubelet")

	// start watcher for dummy device file
	s.startWatcher()

	return nil
}

// Stop stops the gRPC server
func (s *StubDevicePlugin) Stop() error {
	glog.Info("StubDevicePlugin.Stop()")
	if s.grpcServer == nil {
		return nil
	}

	s.grpcServer.Stop()
	s.grpcServer = nil

	return nil
}

func (s *StubDevicePlugin) cleanup() error {
	if err := os.Remove(s.endpoint); err != nil && !os.IsNotExist(err) {
		return err
	}

	return nil
}

func (s *StubDevicePlugin) startWatcher() error {
	// initialize fs watcher with event handler
	err := s.watcher.Init(func(event fsnotify.Event) {
		// Event hander function
		if err := s.handleWatchEvent(event); err != nil {
			glog.Errorf("StubDevicePlugin watch error: %v", err)
		}
	}, func(err error) {
		// TODO(cph): implement error handler
		glog.Errorf("Received an error from watcher: %v", err)
	})

	if err != nil {
		glog.Fatalf("Error initializing watcher: %v", err)
	}

	// add watch path
	if err := s.watcher.AddWatch(s.watcherPath); err != nil {
		glog.Fatalf("Error adding watch path(%s): %v", s.watcherPath, err)
	}

	// start watcher
	s.watcher.Run()

	return nil
}

func (s *StubDevicePlugin) handleWatchEvent(event fsnotify.Event) error {
	// check event.Name is the watched path
	f := path.Base(event.Name)
	reg := regexp.MustCompile(dummyDeviceRE)

	if !reg.MatchString(f) {
		// ignore files don't watched
		return nil
	}

	s.mutex.Lock()
	defer s.mutex.Unlock()

	needUpdate := false
	// create new device
	if event.Op&fsnotify.Create == fsnotify.Create {
		glog.Info("handleWatchEvent...Create")

		// read device state from file
		state, err := ioutil.ReadFile(filepath.Join(s.watcherPath, f))
		if err != nil {
			// ignore read error
			return nil
		}

		// add new device
		s.devices[f] = pluginapi.Device{
			ID:     f,
			Health: strings.Replace(string(state), "\n", "", -1),
		}
		needUpdate = true
	} else if event.Op&fsnotify.Remove == fsnotify.Remove {
		glog.Info("handleWatchEvent...Remove")
		// delete old device
		delete(s.devices, f)
		needUpdate = true
	} else if event.Op&fsnotify.Write == fsnotify.Write {
		glog.Info("handleWatchEvent...Write")

		// read device state from file
		state, err := ioutil.ReadFile(filepath.Join(s.watcherPath, f))
		if err != nil {
			// ignore read error
			return nil
		}

		// update old device
		s.devices[f] = pluginapi.Device{
			ID:     f,
			Health: strings.Replace(string(state), "\n", "", -1),
		}
		needUpdate = true
	}

	if needUpdate {
		s.updateChan <- needUpdate
	}

	return nil
}
