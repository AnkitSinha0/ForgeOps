// Package logger builds the zap logger shared by all ForgeOps services.
package logger

import (
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// New returns a logger whose every line carries service=<service>, so the
// output of controlplane, executor and router stays distinguishable once it
// lands in one log pipeline. Production emits JSON; development emits
// coloured, human-readable lines.
//
// Unlike HashVault there is no package-level global: callers receive the
// logger explicitly, which keeps dependencies visible and tests isolated.
func New(env, service string) (*zap.Logger, error) {
	var cfg zap.Config
	if env == "production" {
		cfg = zap.NewProductionConfig()
	} else {
		cfg = zap.NewDevelopmentConfig()
		cfg.EncoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
	}

	log, err := cfg.Build()
	if err != nil {
		return nil, err
	}
	return log.With(zap.String("service", service)), nil
}
