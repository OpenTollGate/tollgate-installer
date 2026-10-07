// tollgate-installer — cross-platform TollGate router onboarding wizard.
//
// The wizard itself (servers, handlers, UI embed and tests) lives in
// internal/app. This file exists only so the module root still builds the
// binary: `go build -o tollgate-installer .`
package main

import "github.com/OpenTollGate/tollgate-installer/internal/app"

func main() { app.Main() }
