//go:build !windows

package nativeui

import (
	"github.com/kms6402/dodaemon/internal/config"
	"github.com/kms6402/dodaemon/internal/eventbus"
)

type Window struct{}

func New(configPath string, initialCfg *config.Config, bus *eventbus.Bus, onClose func()) (*Window, error) {
	return nil, ErrUnsupported
}

func (w *Window) Run() int                        { return 0 }
func (w *Window) Close()                          {}
func (w *Window) UpdateConfig(cfg *config.Config) {}
