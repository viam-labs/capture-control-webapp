package main

import (
	capturecontrolwebapp "github.com/viam-labs/capture-control-webapp"
	"go.viam.com/rdk/module"
	"go.viam.com/rdk/resource"
	"go.viam.com/rdk/services/generic"
)

func main() {
	module.ModularMain(resource.APIModel{API: generic.API, Model: capturecontrolwebapp.Model})
}
