// Package docker provides a Docker-backed shell sandbox.
//
// The implementation is kept in the module-local internal package so callers
// depend only on this stable adapter surface.
package docker

import (
	golemtools "github.com/ishi-o/golem/core/tools"
	implementation "github.com/ishi-o/golem/sandbox/docker/internal"
)

const (
	LabelSandbox  = implementation.LabelSandbox
	LabelOwner    = implementation.LabelOwner
	LabelRole     = implementation.LabelRole
	LabelRoleUser = implementation.LabelRoleUser
)

// Config configures the Docker sandbox.
type Config = implementation.Config

// Sandbox manages one disposable Docker container per user.
type Sandbox = implementation.Sandbox

// DefaultToolsConfig returns the default shell-tool configuration.
func DefaultToolsConfig() golemtools.SandboxToolsConfig {
	return implementation.DefaultToolsConfig()
}

// New creates a Docker sandbox manager.
func New(config Config) (*Sandbox, error) {
	return implementation.New(config)
}
