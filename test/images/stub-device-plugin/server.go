/*
Copyright 2017 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://wws.apache.org/licenses/LICENSE-2.0

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
	"log"
	"net"
	"os"
	"path"
	"regexp"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"golang.org/x/net/context"
	"google.golang.org/grpc"

	pluginapi "k8s.io/kubernetes/pkg/kubelet/apis/deviceplugin/v1alpha"
	utilfs "k8s.io/kubernetes/pkg/util/filesystem"
)

const (
	dummyDeviceRE string = "^device-[0-9]*$"
)

type StubDevicePlugin struct {
	// grpc info
	endpoint   string
	grpcServer *grpc.Server

	// devices of this DevicePlugin
	mutex   sync.Mutex
	devices map[string]pluginapi.Device

	// dummy device file watcher
	watcher     utilfs.FSWatcher
	watcherPath string
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
			log.Printf("found dummy device %q\n", f.Name())
			devices[f.Name()] = pluginapi.Device{
				ID: f.Name(),
				// TODO(cph): read device state from dummy file
				Health: pluginapi.Healthy,
			}
		}
	}

	log.Printf("getDevices: %+v\n", devices)
	return devices
}

// NewStubDevicePlugin returns an initialized StubDevicePlugin.
func NewStubDevicePlugin(endpoint, dir string) (*StubDevicePlugin, error) {
	devices := getDevices(dir)
	if len(devices) == 0 {
		return nil, fmt.Errorf("No devices found in dir: %s, exit.", dir)
	}

	log.Printf("NewStubDevicePlugin, endpoint: %s, dir: %s", endpoint, dir)
	return &StubDevicePlugin{
		endpoint: endpoint + fmt.Sprintf("-%d", time.Now().Unix()),

		devices: devices,

		watcher:     utilfs.NewFsnotifyWatcher(),
		watcherPath: dir,
	}, nil
}

// Register registers the device plugin for the given resourceName with Kubelet.
func (s *StubDevicePlugin) Register(kubeletEndpoint, resourceName string) error {
	conn, err := grpc.Dial(kubeletEndpoint, grpc.WithInsecure(),
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
		ResourceName: resourceName,
	}

	_, err = client.Register(context.Background(), reqt)
	if err != nil {
		return fmt.Errorf("Cannot register stub worker to kubelet service: %v", err)
	}
	return nil
}

// ListAndWatch lists devices and update that list according to the Update call
func (s *StubDevicePlugin) ListAndWatch(emtpy *pluginapi.Empty, server pluginapi.DevicePlugin_ListAndWatchServer) error {
	for {
		resp := new(pluginapi.ListAndWatchResponse)
		for _, dev := range s.devices {
			resp.Devices = append(resp.Devices, &pluginapi.Device{dev.ID, dev.Health})
		}
		log.Printf("ListAndWatch: send devices %v", resp)

		if err := server.Send(resp); err != nil {
			log.Printf("device-plugin: cannot update device states: %v\n", err)
			return err
		}

		time.Sleep(5 * time.Second)
	}
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

	log.Printf("Allocate response: %+v", response)
	return &response, nil
}

func (s *StubDevicePlugin) Start() error {
	// Start grpc server
	log.Printf("StubDevicePlugin start at: %s", s.endpoint)
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
		s.mutex.Lock()
		server := s.grpcServer
		s.mutex.Unlock()

		if server != nil {
			services := server.GetServiceInfo()
			if len(services) > 0 {
				break
			}
		}

		time.Sleep(1 * time.Second)
	}

	// Register to kubelet
	err = s.Register(pluginapi.KubeletSocket, resourceName)
	if err != nil {
		log.Printf("Could not register device plugin: %s", err)
		s.Stop()
		return err
	}

	log.Printf("Registered device plugin with Kubelet")

	// start dummy device file
	s.startWatcher()

	return nil
}

// Stop stops the gRPC server
func (s *StubDevicePlugin) Stop() error {
	log.Printf("StubDevicePlugin.Stop()\n")
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
			log.Fatalf("StubDevicePlugin watch error: %s", err)
		}
	}, func(err error) {
		// TODO(cph): implement error handler
		log.Fatalf("Received an error from watcher: %s", err)
	})

	if err != nil {
		return fmt.Errorf("Error initializing watcher: %s", err)
	}

	// add watch path
	if err := s.watcher.AddWatch(s.watcherPath); err != nil {
		log.Fatalf("Error adding watch path(%s): %v", err, s.watcherPath)
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

	// create new device
	if event.Op&fsnotify.Create == fsnotify.Create {
		log.Printf("handleWatchEvent...Create")
		s.devices[f] = pluginapi.Device{
			ID: f,
			// TODO(cph): read device state from dummy file
			Health: pluginapi.Healthy,
		}
		return nil
	} else if event.Op&fsnotify.Remove == fsnotify.Remove {
		log.Printf("handleWatchEvent...Remove")
		// delete old device
		delete(s.devices, f)
		return nil
	} else if event.Op&fsnotify.Write == fsnotify.Write {
		log.Printf("handleWatchEvent...Write")
		// update old device
		s.devices[f] = pluginapi.Device{
			ID: f,
			// TODO(cph): read device state from dummy file
			Health: pluginapi.Healthy,
		}
		return nil
	}

	return nil
}
