package main

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/netip"
	"unsafe"

	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/domain"
	"github.com/sagernet/sing/common/varbin"
	"go4.org/netipx"
)

var (
	SRSMagicBytes = [3]byte{0x53, 0x52, 0x53}
)

func SRSFormatToSuffix(format string) string {
	switch format {
	case SRSFormatJSON:
		return ".json"
	case SRSFormatBinary:
		return ".srs"
	}
	panic("unexcepted format: " + format)
}

const (
	SRSRuleItemDomain uint8 = 2 + iota
	SRSRuleItemDomainKeyword
	SRSRuleItemDomainRegex
	SRSRuleItemIPCIDR uint8 = 6

	SRSRuleItemFinal uint8 = 0xFF

	SRSCurrentVersion uint8 = 2
)

const (
	SRSFormatBinary = "binary"
	SRSFormatJSON   = "json"
)

type SrsJSONRuleset struct {
	Domain        []string `json:"domain,omitempty"`
	DomainSuffix  []string `json:"domain_suffix,omitempty"`
	DomainKeyword []string `json:"domain_keyword,omitempty"`
	DomainRegexp  []string `json:"domain_regex,omitempty"`
	IPCidr        []string `json:"ip_cidr,omitempty"`
}

type SrsGenerator struct {
	Domain *DomainRuleset
	IP     *IPRuleset

	Version uint8
}

func (generator *SrsGenerator) Marshal(format string) ([]byte, error) {
	switch format {
	case SRSFormatBinary:
		return generator.MarshalBinary()
	case SRSFormatJSON:
		return generator.MarshalJSON()
	}
	return nil, fmt.Errorf("unsupported format: %s", format)
}

func (generator *SrsGenerator) MarshalJSON() ([]byte, error) {
	type schema struct {
		Version uint8            `json:"version,omitempty"`
		Rules   []SrsJSONRuleset `json:"rules,omitempty"`
	}
	J := schema{}
	R := SrsJSONRuleset{}

	if generator.Domain != nil {
		R.Domain = generator.Domain.Domain
		R.DomainSuffix = generator.Domain.Suffix
		R.DomainKeyword = generator.Domain.KeyWord
		R.DomainRegexp = generator.Domain.Regexp
	}
	if generator.IP != nil && generator.IP.Count() > 0 {
		R.IPCidr = common.Map(generator.IP.Set.Prefixes(), netip.Prefix.String)
	}
	J.Version = generator.Version
	J.Rules = append(J.Rules, R)
	return json.MarshalIndent(J, "", "  ")
}

func (generator *SrsGenerator) MarshalBinary() ([]byte, error) {
	buffer := bytes.NewBuffer(nil)
	buffer.Write(SRSMagicBytes[:])
	_ = binary.Write(buffer, binary.BigEndian, generator.Version)
	zlibWriter, err := zlib.NewWriterLevel(buffer, zlib.BestCompression)
	if err != nil {
		return nil, err
	}
	count := uint64(0)
	if generator.Domain != nil && generator.Domain.Count() > 0 {
		count++
	}
	if generator.IP != nil && generator.IP.Count() > 0 {
		count++
	}
	if count == 0 {
		return nil, ErrEmpty
	}

	bufWriter := bufio.NewWriter(zlibWriter)
	_, err = varbin.WriteUvarint(bufWriter, uint64(count))
	if err != nil {
		return nil, err
	}
	if generator.Domain != nil && generator.Domain.Count() > 0 {
		err := generator.marshalSRSDomain(bufWriter)
		if err != nil {
			return nil, fmt.Errorf("marshal domain: %w", err)
		}
	}

	if generator.IP != nil && generator.IP.Count() > 0 {
		err := generator.marshalSRSIP(bufWriter)
		if err != nil {
			return nil, fmt.Errorf("marshal ip: %w", err)
		}
	}
	if err := bufWriter.Flush(); err != nil {
		return nil, err
	}
	if err := zlibWriter.Close(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func (generator *SrsGenerator) marshalSRSIP(w varbin.Writer) error {
	err := binary.Write(w, binary.BigEndian, uint8(0))
	if err != nil {
		return err
	}
	err = binary.Write(w, binary.BigEndian, SRSRuleItemIPCIDR)
	if err != nil {
		return err
	}
	err = srsWriteIPSet(w, generator.IP.Set)
	if err != nil {
		return err
	}
	err = binary.Write(w, binary.BigEndian, SRSRuleItemFinal)
	if err != nil {
		return err
	}
	// err = binary.Write(writer, binary.BigEndian, rule.Invert)
	return binary.Write(w, binary.BigEndian, false)
}

func (generator *SrsGenerator) marshalSRSDomain(w varbin.Writer) error {
	err := binary.Write(w, binary.BigEndian, uint8(0))
	if err != nil {
		return err
	}
	if len(generator.Domain.Domain) > 0 || len(generator.Domain.Suffix) > 0 {
		err = binary.Write(w, binary.BigEndian, SRSRuleItemDomain)
		if err != nil {
			return err
		}
		err = domain.NewMatcher(generator.Domain.Domain, generator.Domain.Suffix, false).Write(w)
		if err != nil {
			return err
		}
	}
	if len(generator.Domain.KeyWord) > 0 {
		err = srsWriteRuleItemString(w, SRSRuleItemDomainKeyword, generator.Domain.KeyWord)
		if err != nil {
			return err
		}
	}
	if len(generator.Domain.Regexp) > 0 {
		err = srsWriteRuleItemString(w, SRSRuleItemDomainRegex, generator.Domain.Regexp)
		if err != nil {
			return err
		}
	}
	err = binary.Write(w, binary.BigEndian, SRSRuleItemFinal)
	if err != nil {
		return err
	}
	// err = binary.Write(writer, binary.BigEndian, rule.Invert)
	return binary.Write(w, binary.BigEndian, false)
}

func (rule *DomainRuleset) WriteSRS(w io.Writer, format string) error {
	if rule.Count() == 0 {
		return ErrEmpty
	}
	generator := &SrsGenerator{Domain: rule, Version: SRSCurrentVersion}
	gen, err := generator.Marshal(format)
	if err != nil {
		return err
	}
	_, err = w.Write(gen)
	return err
}

func (rule *IPRuleset) WriteSRS(w io.Writer, format string) error {
	if rule.Count() == 0 {
		return ErrEmpty
	}
	generator := &SrsGenerator{IP: rule, Version: SRSCurrentVersion}
	gen, err := generator.Marshal(format)
	if err != nil {
		return err
	}
	_, err = w.Write(gen)
	return err
}

func srsWriteRuleItemString(writer varbin.Writer, itemType uint8, value []string) error {
	err := writer.WriteByte(itemType)
	if err != nil {
		return err
	}
	_, err = varbin.WriteUvarint(writer, uint64(len(value)))
	if err != nil {
		return err
	}
	for _, s := range value {
		_, err = varbin.WriteUvarint(writer, uint64(len(s)))
		if err != nil {
			return err
		}
		_, err = writer.Write([]byte(s))
		if err != nil {
			return err
		}
	}
	return nil
}

func srsWriteIPSet(w varbin.Writer, set *netipx.IPSet) error {
	err := w.WriteByte(1)
	if err != nil {
		return err
	}

	mySet := (*myIPSet)(unsafe.Pointer(set))
	err = binary.Write(w, binary.BigEndian, uint64(len(mySet.rr)))
	if err != nil {
		return err
	}
	for _, rr := range mySet.rr {
		fromBytes := rr.from.AsSlice()
		_, err = varbin.WriteUvarint(w, uint64(len(fromBytes)))
		if err != nil {
			return err
		}
		_, err = w.Write(fromBytes)
		if err != nil {
			return err
		}
		toBytes := rr.to.AsSlice()
		_, err = varbin.WriteUvarint(w, uint64(len(toBytes)))
		if err != nil {
			return err
		}
		_, err = w.Write(toBytes)
		if err != nil {
			return err
		}
	}
	return nil
}

type dummyVarbinWriter struct {
	io.Writer
}

func (d *dummyVarbinWriter) WriteByte(c byte) error {
	_, err := d.Writer.Write([]byte{c})
	return err
}
