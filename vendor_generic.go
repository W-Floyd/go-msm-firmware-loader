package msmfw

import (
	"errors"
	"path/filepath"
)

// genericVendor is driven by Config.Rules: every named partition is mounted
// (read-only) unless Config.Partitions names some, and each rule's file is
// found in them and placed in the target. It knows nothing of any family; a
// vendor that does (qcom) is quicker and cleverer, and this is for the device
// none knows yet.
type genericVendor struct{}

func (genericVendor) Name() string { return "generic" }

func (genericVendor) Validate(cfg Config) error {
	if cfg.Compat {
		return errors.New("generic vendor: Compat is the Qualcomm script's")
	}
	if len(cfg.Rules) == 0 {
		return errors.New("generic vendor: no rules (Config.Rules, --rule)")
	}
	return nil
}

// Partitions is every named partition: rules look everywhere.
func (genericVendor) Partitions(Config) []string { return nil }

// SuperPartitions: an Android device's dynamic partitions are looked for too,
// so rules reach into them as into any other partition.
func (genericVendor) SuperPartitions(Config) []string { return DefaultSuperPartitions() }

// CopyCapable: the files are placed through Env.Place.
func (genericVendor) CopyCapable() {}

func (genericVendor) Link(env *Env) error {
	cfg := env.Config()
	env.LinkExtra()
	// One walk of each partition, in mount order.
	var parts [][]string
	for _, m := range env.Mounted() {
		parts = append(parts, env.Walk(m, cfg.MaxDepth))
	}
	for _, rule := range cfg.Rules {
		n := 0
		seen := map[string]bool{}
	search:
		for _, files := range parts {
			for _, f := range files {
				base := filepath.Base(f)
				if !rule.matches(base) || seen[base] {
					continue
				}
				seen[base] = true
				dst := rule.Dst
				if rule.isDir() {
					dst = pathJoin(dst, base)
				}
				env.Place(f, dst)
				n++
				if !rule.isDir() {
					break search
				}
			}
		}
		if n == 0 {
			env.Logf("rule %s: no file found", rule)
			env.Missing(rule)
		}
	}
	return nil
}
