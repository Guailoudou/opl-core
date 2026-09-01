# Embedded OpenP2P notice

This directory contains the OpenP2P TCP/UDP port-tunnel implementation migrated from
the local OpenP2P working tree based on `openp2p-cn/openp2p` commit
`232fb31200c4d9de6ce7ea8ca54d28d7a504f893` (protocol version `3.25.11`).
The local lifecycle/shutdown changes present at migration time are included. It is
distributed under the OpenP2P MIT license.

OPL intentionally excludes the upstream command-line program, daemon/service installer,
automatic updater, and SD-WAN/TUN implementation. OPL supplies its own lifecycle,
WireGuard network, pairing, address allocation, and LAN discovery relay.

The original copyright and license are shipped as `openp2p-LICENSE.txt` in the
application package. Copyright (c) 2021 OpenP2P.cn.

The `upnp` subdirectory retains go-ethereum file notices and is accompanied by
`go-ethereum-LGPL-3.0.txt` in the application package.
