package main

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/okian/servo/v3/internal/emit"
	"github.com/okian/servo/v3/internal/graph"
	"github.com/okian/servo/v3/internal/load"
	"github.com/okian/servo/v3/internal/render"
	"github.com/okian/servo/v3/internal/resolve"
)

// companionFiles is every generated config loader this graph needs, keyed
// by output path: one servo_config_gen.go beside each used //servo:config
// type. withFile decides the loader's signature — it must match what the
// injector's generated New passes, which is why checkConfigAgreement runs
// before anything calls this.
func companionFiles(resolved *resolve.Resolved, withFile bool) (map[string][]byte, error) {
	out := map[string][]byte{}
	for _, n := range resolved.Configs {
		content, err := emit.EmitConfigLoader(n.Config, withFile)
		if err != nil {
			// The only failure is a formatting bug in servo itself, which
			// EmitConfigLoader's error already says.
			return nil, err
		}
		out[filepath.Join(n.Config.Dir, emit.ConfigLoaderFileName)] = content
	}
	return out, nil
}

// checkConfigAgreement refuses a module whose injectors disagree about
// whether a shared config's loader reads a config file. The companion is
// one file with one signature — ServoConfig(file map[string]any) when a
// servo.ConfigFile is declared, ServoConfig() when not — so every injector
// that uses the config has to make the same choice. resolveds is parallel
// to pipelines, with nil for injectors whose resolution already failed
// (they are already reported; their usage cannot be known).
func checkConfigAgreement(pipelines []*pipeline, resolveds []*resolve.Resolved) error {
	type usage struct {
		withFile    bool
		injectorPkg string
	}
	first := map[graph.Key]usage{}
	var errs []error
	for i, p := range pipelines {
		if resolveds[i] == nil {
			continue
		}
		withFile := p.spec.ConfigFile != nil
		for _, n := range resolveds[i].Configs {
			prior, seen := first[n.Key]
			if !seen {
				first[n.Key] = usage{withFile, p.spec.InjectorPkg.PkgPath}
				continue
			}
			if prior.withFile == withFile {
				continue
			}
			with, without := prior.injectorPkg, p.spec.InjectorPkg.PkgPath
			if withFile {
				with, without = without, with
			}
			errs = append(errs, fmt.Errorf(
				"servo: %s is used by %s (which declares servo.ConfigFile) and by %s (which does not) — its generated loader is one file with one signature, so declare a ConfigFile in both injectors or neither",
				n.Key.String(), with, without))
		}
	}
	return errors.Join(errs...)
}

// configSettingJSON is one row of `servo config --json`.
type configSettingJSON struct {
	Env      string `json:"env"`
	FileKey  string `json:"file_key,omitempty"`
	Type     string `json:"type"`
	Required bool   `json:"required"`
	Default  string `json:"default,omitempty"`
	Secret   bool   `json:"secret"`
	Field    string `json:"field"`
	Pos      string `json:"pos"`
}

// runConfig prints the operator's manual for one injector: every setting
// its graph reads, with the environment variable, the config-file key when
// a file is declared, the type, and whether it is required, defaulted, or
// secret. The generator already knows all of it, which is the one thing a
// runtime-reflection config library can never say without running the
// binary.
func runConfig(cfg load.Config, jsonOut bool) error {
	p, err := buildPipeline(cfg)
	if err != nil {
		return err
	}
	resolved, err := p.resolve(nil)
	if err != nil {
		return err
	}

	withFile := p.spec.ConfigFile != nil
	modRoot := moduleRoot(p.spec)
	var rows []configSettingJSON
	for _, n := range resolved.Configs {
		for _, f := range n.Config.Fields {
			row := configSettingJSON{
				Env:      f.EnvName,
				Type:     f.Kind.GoType(),
				Required: f.Required,
				Secret:   f.Secret,
				Field:    n.Config.TypeName + "." + f.FieldName,
				Pos:      render.RelTo(modRoot, f.Pos.String()),
			}
			if withFile {
				row.FileKey = n.Config.Section + "." + f.FileKey
			}
			if f.HasDefault {
				row.Default = f.Default
			}
			rows = append(rows, row)
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Env < rows[j].Env })

	if jsonOut {
		return printJSON(rows)
	}

	fmt.Printf("configuration for %s\n", p.spec.InjectorPkg.PkgPath)
	if withFile {
		fmt.Printf("file: %s (override the path with %s; environment always wins)\n", p.spec.ConfigFile.Path, emit.ConfigFileEnvVar)
	}
	if len(rows) == 0 {
		fmt.Println("  no //servo:config types are in this graph")
		return nil
	}
	fmt.Println()

	header := []string{"ENV"}
	if withFile {
		header = append(header, "FILE KEY")
	}
	header = append(header, "TYPE", "MISSING?", "FIELD")
	table := [][]string{header}
	for _, row := range rows {
		missing := "zero value"
		switch {
		case row.Required:
			missing = "required"
		case row.Default != "":
			missing = "default " + row.Default
		}
		typ := row.Type
		if row.Secret {
			typ += " (secret)"
		}
		cols := []string{row.Env}
		if withFile {
			cols = append(cols, row.FileKey)
		}
		cols = append(cols, typ, missing, row.Field+"  "+row.Pos)
		table = append(table, cols)
	}
	printColumns(table)
	return nil
}

// printColumns renders rows with each column padded to its widest cell.
func printColumns(rows [][]string) {
	widths := make([]int, len(rows[0]))
	for _, row := range rows {
		for i, cell := range row {
			if len(cell) > widths[i] {
				widths[i] = len(cell)
			}
		}
	}
	for _, row := range rows {
		var b strings.Builder
		b.WriteString(" ")
		for i, cell := range row {
			fmt.Fprintf(&b, " %-*s", widths[i], cell)
		}
		fmt.Println(strings.TrimRight(b.String(), " "))
	}
}
