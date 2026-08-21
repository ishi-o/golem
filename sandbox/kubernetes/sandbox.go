// Package kubernetes provides a Kubernetes-backed shell sandbox.
//
// The implementation is kept in the module-local internal package so callers
// depend only on this stable adapter surface.
package kubernetes

import (
	"github.com/ishi-o/golem/core/tools"
	implementation "github.com/ishi-o/golem/sandbox/kubernetes/internal"
)

const (
	LabelSandbox      = implementation.LabelSandbox
	LabelOwner        = implementation.LabelOwner
	LabelRole         = implementation.LabelRole
	LabelRoleUser     = implementation.LabelRoleUser
	ContainerName     = implementation.ContainerName
	CredentialsVolume = implementation.CredentialsVolume
)

// PVCMount describes a persistent volume exposed to a user sandbox.
type PVCMount = implementation.PVCMount

// Config configures the Kubernetes sandbox.
type Config = implementation.Config

// Sandbox manages one disposable Kubernetes Job/Pod per user.
type Sandbox = implementation.Sandbox

// DefaultToolsConfig returns the default shell-tool configuration.
func DefaultToolsConfig() tools.SandboxToolsConfig {
	return implementation.DefaultToolsConfig()
}

// New creates a Kubernetes sandbox manager.
func New(config Config) (*Sandbox, error) {
	return implementation.New(config)
}
