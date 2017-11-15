load("@io_bazel_rules_go//go:def.bzl", "go_binary", "go_library")

go_library(
    name = "go_default_library",
    srcs = [
        "main.go",
        "server.go",
    ],
    importpath = "k8s.io/kubernetes/test/images/stub-device-plugin",
    visibility = ["//visibility:private"],
    deps = [
        "//pkg/kubelet/apis/deviceplugin/v1alpha:go_default_library",
        "//pkg/util/filesystem:go_default_library",
        "//vendor/github.com/fsnotify/fsnotify:go_default_library",
        "//vendor/github.com/golang/glog:go_default_library",
        "//vendor/golang.org/x/net/context:go_default_library",
        "//vendor/google.golang.org/grpc:go_default_library",
    ],
)

go_binary(
    name = "stub-device-plugin",
    importpath = "k8s.io/kubernetes/test/images/stub-device-plugin",
    library = ":go_default_library",
    visibility = ["//visibility:public"],
)

filegroup(
    name = "package-srcs",
    srcs = glob(["**"]),
    tags = ["automanaged"],
    visibility = ["//visibility:private"],
)

filegroup(
    name = "all-srcs",
    srcs = [":package-srcs"],
    tags = ["automanaged"],
    visibility = ["//visibility:public"],
)
