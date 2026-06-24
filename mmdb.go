package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/maxmind/mmdbwriter"
	"github.com/maxmind/mmdbwriter/mmdbtype"
	"go4.org/netipx"
)

const mmdbDatabaseType = "GeoIP2-Country"

// WriteMergedMMDB builds a single MaxMind database from every IP entry and
// writes it to w. Each entry's prefixes are stored as country.iso_code set to
// the (upper-cased) entry name. More specific codes inserted later (e.g. the
// operator subdivisions cn-cm/cn-ct/cn-cu) override the broader country (cn)
// on the ranges they overlap.
func WriteMergedMMDB(w io.Writer, entries []Entry) error {
	writer, err := mmdbwriter.New(mmdbwriter.Options{
		DatabaseType: mmdbDatabaseType,
		RecordSize:   24,
	})
	if err != nil {
		return err
	}

	for _, e := range entries {
		if e.Type != "ip" {
			continue
		}
		ipr, ok := e.Ruleset.(*IPRuleset)
		if !ok || ipr.Set == nil {
			continue
		}

		record := mmdbtype.Map{
			"country": mmdbtype.Map{
				"iso_code": mmdbtype.String(strings.ToUpper(e.Name)),
			},
		}
		for _, p := range ipr.Set.Prefixes() {
			if err = writer.Insert(netipx.PrefixIPNet(p), record); err != nil {
				return fmt.Errorf("insert %s (%s): %w", p, e.Name, err)
			}
		}
	}

	_, err = writer.WriteTo(w)
	return err
}