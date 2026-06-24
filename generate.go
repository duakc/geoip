package main

import (
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

var (
	silentWarn = os.Getenv("RULESET_SILENT_WARN") != ""

	generateSRS  bool
	generateMRS  bool
	generateMMDB bool
	generateAll  bool
	providerPath string
	outputPath   string
)

type Entry struct {
	Ruleset RuleSet
	Name    string
	Type    string
}

func main() {
	flag.BoolVar(&generateSRS, "srs", false, "Generate SRS")
	flag.BoolVar(&generateMRS, "mrs", false, "Generate MRS")
	flag.BoolVar(&generateMMDB, "mmdb", false, "Generate merged MMDB")
	flag.BoolVar(&generateAll, "all", false, "Generate all format SRS/MRS")
	flag.StringVar(&providerPath, "from", "./data/", "Setup datasource")
	flag.StringVar(&outputPath, "output", "./output/", "Setup output")
	flag.Usage = func() {
		_, _ = fmt.Fprintf(flag.CommandLine.Output(), "Usage of %s:\n", "ruleset-generator")
		flag.PrintDefaults()
	}
	flag.Parse()
	if !generateSRS && !generateMRS && !generateMMDB {
		flag.Usage()
		return
	}
	var entries []Entry
	err := filepath.WalkDir(providerPath, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !entry.Type().IsRegular() {
			return err
		}
		var (
			ruleset RuleSet
			typ     string
		)
		switch filepath.Base(filepath.Dir(path)) {
		case "domain":
			typ = "domain"
			ruleset, err = NewDomainFile(path)
			if err != nil {
				return err
			}
		case "ip":
			typ = "ip"
			ruleset, err = NewIPFile(path)
			if err != nil {
				return err
			}
		default:
			_, _ = fmt.Fprintf(os.Stderr, "[ERROR] unable to determined rule type for %s\n", path)
			return nil
		}
		entries = append(entries, Entry{Ruleset: ruleset, Name: entry.Name(), Type: typ})

		return nil
	})
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "[ERROR] %s\n", err.Error())
		return
	}
	if generateSRS {
		err := generateSRSFunc(entries)
		if err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "[ERROR] %s\n", err.Error())
			return
		}
	}
	if generateMRS {
		err := generateMRSFunc(entries)
		if err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "[ERROR] %s\n", err.Error())
			return
		}
	}
	if generateMMDB {
		err := generateMMDBFunc(entries)
		if err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "[ERROR] %s\n", err.Error())
			return
		}
	}

}

func generateMMDBFunc(entries []Entry) error {
	path := filepath.Join(outputPath, "ip", "mmdb", "geoip.mmdb")
	return openWrite(path, func(w io.Writer) error {
		return WriteMergedMMDB(w, entries)
	})
}

func generateSRSFunc(entries []Entry) error {
	formatList := []string{SRSFormatBinary}
	if generateAll {
		formatList = append(formatList, SRSFormatJSON)
	}
	for _, E := range entries {
		for _, F := range formatList {
			path := filepath.Join(outputPath, E.Type, "srs", E.Name+SRSFormatToSuffix(F))

			if err := openWrite(path, func(w io.Writer) error {
				return E.Ruleset.WriteSRS(w, F)
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

func generateMRSFunc(entries []Entry) error {
	for _, E := range entries {
		var (
			behaviors []int

			formats = []string{MRSFormatBinary}
		)

		switch E.Type {
		case "ip":
			behaviors = []int{MRSRuleBehaviorIP}
		case "domain":
			behaviors = []int{MRSRuleBehaviorDomain}
		}
		if generateAll {
			behaviors = append(behaviors, MRSRuleBehaviorClassical)
			formats = append(formats, MRSFormatText, MRSFormatYAML)
		}
		for _, B := range behaviors {
			for _, F := range formats {
				if B == MRSRuleBehaviorClassical && F == MRSFormatBinary {
					//_ ,_ = fmt.Fprintf(os.Stderr,
					//	"[WARN] MRSFormatBinary doesn't support MRSRuleBehaviorClassical behavior: type=%s name=%s\n",
					//	E.Type,E.Name)
					continue
				}
				path := filepath.Join(outputPath, E.Type, "mrs", E.Name+MRSFormatToSuffix(F, B))

				if err := openWrite(path, func(w io.Writer) error {
					return E.Ruleset.WriteMRS(w, F, B)
				}); err != nil {
					if errors.Is(err, ErrDomainNotSupport) {
						if !silentWarn {
							_, _ = fmt.Fprintf(os.Stderr,
								"[WARN] %s, so %s/%s will only generate behavior-classical file: generate %s\n",
								err.Error(), E.Type, E.Name, path)
						}

						continue
					}
					return err
				}
			}
		}
	}
	return nil
}

func openWrite(path string, then func(w io.Writer) error) error {
	err := os.MkdirAll(filepath.Dir(path), 0777)
	if err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	output, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0666)
	if err != nil {
		return err
	}
	defer output.Close()

	return then(output)
}

type writeBinaryOperation struct {
	Bytes  []byte
	Binary any
	Ending binary.ByteOrder
}

func writeGuard(w io.Writer, wbos ...writeBinaryOperation) error {
	var err error
	for i := 0; i < len(wbos) && err == nil; i++ {
		op := wbos[i]
		if op.Ending != nil {
			err = binary.Write(w, op.Ending, op.Binary)
		}
		if err != nil {
			break
		}
		if len(op.Bytes) != 0 {
			_, err = w.Write(op.Bytes)
		}
	}
	return err
}
