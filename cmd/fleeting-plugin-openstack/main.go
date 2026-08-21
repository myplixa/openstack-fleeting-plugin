package main

import (
	osplugin "github.com/myplixa/openstack-fleeting-plugin"
	"gitlab.com/gitlab-org/fleeting/fleeting/plugin"
)

func main() {
	plugin.Serve(&osplugin.InstanceGroup{})
}
