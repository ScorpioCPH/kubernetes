## Stub Device Plugin

### build and push

```shell
$ cd k8s.io/kubernetes/test/images
$ make all-push WHAT=stub-device-plugin
```
### config file

we use [config file](./config.json) for device representation:

```JSON
{
	"items":[
		{
			"resourceName":"resource.name/1",
			"devices":[
				{"ID":"device-1","health":"Healthy"},
				{"ID":"device-2","health":"Healthy"}
			]
		},
		{
			"resourceName":"resource.name/2",
			"devices":[
				{"ID":"device-1","health":"Healthy"},
				{"ID":"device-2","health":"Healthy"},
				{"ID":"device-3","health":"Unhealthy"}
			]
		}
	]
}

```

e2e test will read this config file to create dummy device files as following structure:

```text
|-- tmp
     |-- dummy-device
         |-- resource.name-1
             |-- device-1 (content: healthy)
             |-- device-2 (content: unhealthy)
         |-- resource.name-2
             |-- device-1 (content: healthy)
             |-- device-2 (content: healthy)
             |-- device-3 (content: unhealthy)
         |-- resource.name-3
             |-- device-1 (content: healthy)
             |-- device-2 (content: healthy)
```

we can add more devices and resources by changing the config file.
