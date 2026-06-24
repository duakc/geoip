package main

import "net/netip"

type myIPRange struct {
	from netip.Addr
	to   netip.Addr
}
type myIPSet struct {
	rr []myIPRange
}
