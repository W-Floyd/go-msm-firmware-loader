package msmfw

import (
	"errors"
	"strings"
)

// qcomVendor is the Qualcomm linking of postmarketOS's msm-firmware-loader
// (link.go): the partitions it takes blobs from (with the A/B slot suffix),
// "super" and "system" for Android's dynamic partitions, and the script's
// steps from image/* to the .mdt/.mbn links. It is what the package was
// before the vendor seam, and the default.
type qcomVendor struct{}

func (qcomVendor) Name() string { return "qcom" }

func (qcomVendor) Validate(cfg Config) error {
	if len(cfg.Rules) > 0 || cfg.Copy || cfg.MaxDepth != 0 {
		return errors.New("qcom vendor: rules, Copy and MaxDepth are the generic vendor's")
	}
	return nil
}

func (qcomVendor) Partitions(cfg Config) []string { return DefaultPartitions(cfg.SlotSuffix) }

func (qcomVendor) SuperPartitions(Config) []string { return DefaultSuperPartitions() }

// CheckPartitions warns of an A/B device with no slot suffix: the firmware
// partitions are named with one, so none would be found.
func (qcomVendor) CheckPartitions(cfg Config, names []string, logf func(string, ...any)) {
	if cfg.SlotSuffix != "" || cfg.Compat || cfg.Partitions != nil {
		return
	}
	for _, n := range names {
		if strings.HasSuffix(n, "_a") || strings.HasSuffix(n, "_b") {
			logf("warning: no slot suffix (androidboot.slot_suffix), but partition %q is A/B; the firmware partitions will not be found", n)
			return
		}
	}
}

func (qcomVendor) Link(env *Env) error { return env.r.link() }
