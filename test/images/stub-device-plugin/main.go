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
	"flag"
	"log"
	"os"
	"os/signal"
	"path"
	"syscall"

	"github.com/fsnotify/fsnotify"
	pluginapi "k8s.io/kubernetes/pkg/kubelet/apis/deviceplugin/v1alpha"
)

var (
	resourceName   = ""
	endpointPath   = ""
	dummyDeviceDir = ""
)

func init() {
	defaultEndpointPath := pluginapi.DevicePluginPath + "stub.sock"
	flag.StringVar(&resourceName, "resource_name", "dummy.device.com/device", "Dummy device resource name to be advertised to Kubelet")
	flag.StringVar(&endpointPath, "endpoint_path", defaultEndpointPath, "Path to stub device plugin grpc endpoint")
	flag.StringVar(&dummyDeviceDir, "dummy_device_dir", "dummy-device", "Path to read dummy device file from")
}

func main() {
	flag.Parse()

	// create new stub device plugin with endpoint and tmp dir
	dir := path.Join(os.TempDir(), dummyDeviceDir)
	stubDevicePlugin, err := NewStubDevicePlugin(endpointPath, dir)
	if err != nil {
		log.Panicln("Fatal to create stub device plugin:", err)
	}

	// start serving
	stubDevicePlugin.Start()

	// watcher for Kubelet
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Panicln("Fatal:", err)
	}

	defer watcher.Close()
	err = watcher.Add(pluginapi.DevicePluginPath)
	if err != nil {
		log.Panicln("Fatal:", err)
	}

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)

L:
	for {
		restart := false
		select {
		// kubelet socket changed
		case event := <-watcher.Events:
			if event.Name == pluginapi.KubeletSocket &&
				(event.Op&fsnotify.Create == fsnotify.Create ||
					event.Op&fsnotify.Write == fsnotify.Write) {
				log.Printf("inotify: %s changed, restarting", pluginapi.KubeletSocket)
				restart = true
			}
		case err := <-watcher.Errors:
			log.Printf("inotify: %s", err)
		// system sigs
		case s := <-sigs:
			switch s {
			case syscall.SIGHUP:
				log.Println("Received SIGHUP, restarting")
				restart = true
			default:
				log.Printf("Received signal %d, shutting down", s)
				stubDevicePlugin.Stop()
				break L
			}
		}
		if restart {
			log.Println("restarting...")
			stubDevicePlugin.Stop()
			stubDevicePlugin, err = NewStubDevicePlugin(endpointPath, dir)
			if err != nil {
				log.Panicln("Fatal to create stub device plugin:", err)
			}

			stubDevicePlugin.Start()
		}
	}
}
