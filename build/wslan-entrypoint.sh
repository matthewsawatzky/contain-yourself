#!/bin/sh
set -eu

role=${WSLAN_ROLE:-gateway}
if [ "$role" = "sandbox" ]; then
  : "${WSLAN_GATEWAY:?set WSLAN_GATEWAY}"
  ip route replace default via "$WSLAN_GATEWAY"
  exec sleep infinity
fi
if [ "$role" != "gateway" ]; then
  echo "unsupported WSLAN_ROLE: $role" >&2
  exit 1
fi

: "${WSLAN_INTERNAL_CIDR:?set WSLAN_INTERNAL_CIDR}"
: "${WSLAN_MODE:?set WSLAN_MODE}"

internal_interface=$(ip -o route show "$WSLAN_INTERNAL_CIDR" | awk 'NR == 1 {print $3}')
external_interface=$(ip -o route show default | awk 'NR == 1 {print $5}')
if [ -z "$internal_interface" ] || [ -z "$external_interface" ]; then
  echo "could not discover WSLAN interfaces" >&2
  exit 1
fi
internal_address=$(ip -o -4 addr show dev "$internal_interface" | awk 'NR == 1 {split($4, value, "/"); print value[1]}')
export WSLAN_INTERNAL_ADDRESS="$internal_address"
management_cidr=$(ip -o -4 route show dev "$external_interface" scope link |
  awk '$1 ~ /\// {print $1; exit}')
if [ -z "$internal_address" ]; then
  echo "could not discover WSLAN internal address" >&2
  exit 1
fi

[ "$(cat /proc/sys/net/ipv4/ip_forward)" = "1" ] || {
  echo "WSLAN requires net.ipv4.ip_forward=1" >&2
  exit 1
}
iptables -P FORWARD DROP
iptables -F FORWARD
iptables -t nat -F POSTROUTING
iptables -t mangle -F OUTPUT
iptables -A INPUT -i "$internal_interface" -p tcp --dport 9000 -j DROP

host_gateway=$(ip -o route show default | awk 'NR == 1 {print $3}')

# drop_control_plane blocks the workstation from reaching the controller, the
# worker, or any other workstation's gateway on the management network. Every
# non-VPN mode calls it; wireguard does not need it because nothing is routed
# out of the management interface at all.
drop_control_plane() {
  if [ -n "$management_cidr" ]; then
    iptables -A FORWARD -i "$internal_interface" -d "$management_cidr" -j DROP
  fi
}

dns_arguments="--resolv-file=/etc/resolv.conf"
case "$WSLAN_MODE" in
  direct)
    output_interface=$external_interface
    drop_control_plane
    ;;
  host-gateway)
    output_interface=$external_interface
    # Reach services listening on the Docker host, but nothing else on the
    # management network. Order matters: this ACCEPT is appended before the
    # DROP below, and iptables takes the first match.
    if [ -n "$host_gateway" ]; then
      iptables -A FORWARD -i "$internal_interface" -d "$host_gateway" -j ACCEPT
    else
      echo "host-gateway mode could not discover the host address" >&2
      exit 1
    fi
    drop_control_plane
    ;;
  ipv6)
    output_interface=$external_interface
    drop_control_plane
    if [ ! -d /proc/sys/net/ipv6 ]; then
      echo "ipv6 mode requires IPv6 on the Docker daemon (set ipv6 and fixed-cidr-v6 in daemon.json)" >&2
      exit 1
    fi
    internal_cidr6=$(ip -o -6 route show dev "$internal_interface" 2>/dev/null |
      awk '$1 ~ /\// && $1 !~ /^fe80/ {print $1; exit}')
    if [ -z "$internal_cidr6" ]; then
      echo "ipv6 mode found no IPv6 subnet on the workstation network" >&2
      exit 1
    fi
    ip6tables -P FORWARD DROP
    ip6tables -F FORWARD
    ip6tables -t nat -F POSTROUTING 2>/dev/null || true
    ip6tables -A INPUT -i "$internal_interface" -p tcp --dport 9000 -j DROP
    management_cidr6=$(ip -o -6 route show dev "$external_interface" 2>/dev/null |
      awk '$1 ~ /\// && $1 !~ /^fe80/ {print $1; exit}')
    if [ -n "$management_cidr6" ]; then
      ip6tables -A FORWARD -i "$internal_interface" -d "$management_cidr6" -j DROP
    fi
    ip6tables -A FORWARD -i "$internal_interface" -o "$external_interface" -j ACCEPT
    ip6tables -A FORWARD -i "$external_interface" -o "$internal_interface" \
      -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT
    ip6tables -t nat -A POSTROUTING -s "$internal_cidr6" -o "$external_interface" -j MASQUERADE
    ;;
  wireguard)
    config=/run/wslan/wg0.conf
    [ -s "$config" ] || {
      echo "WireGuard mode requires $config" >&2
      exit 1
    }
    sanitized=/tmp/wg0.conf
    umask 077
    awk '
      tolower($0) ~ /^[[:space:]]*dns[[:space:]]*=/ { next }
      tolower($0) ~ /^[[:space:]]*\[interface\][[:space:]]*$/ {
        print
        print "Table = off"
        next
      }
      { print }
    ' "$config" >"$sanitized"
    dns_servers=$(awk -F= 'tolower($1) ~ /^[[:space:]]*dns[[:space:]]*$/ {
      gsub(/[[:space:]]/, "", $2); print $2
    }' "$config" | tr ',' '\n')
    wg-quick up "$sanitized"
    output_interface=wg0
    route_table=51821
    ip route add unreachable default table "$route_table" metric 42760
    ip route add default dev wg0 table "$route_table" metric 1
    ip rule add fwmark "$route_table" table "$route_table" priority 99
    ip rule add from "$WSLAN_INTERNAL_CIDR" table "$route_table" priority 100
    iptables -t mangle -A OUTPUT -p udp --dport 53 -j MARK --set-mark "$route_table"
    iptables -t mangle -A OUTPUT -p tcp --dport 53 -j MARK --set-mark "$route_table"
    dns_arguments="--no-resolv"
    if [ -n "$dns_servers" ]; then
      for server in $dns_servers; do
        dns_arguments="$dns_arguments --server=$server"
      done
    else
      dns_arguments="$dns_arguments --server=1.1.1.1 --server=1.0.0.1"
    fi
    ;;
  *)
    echo "unsupported WSLAN_MODE: $WSLAN_MODE" >&2
    exit 1
    ;;
esac

iptables -A FORWARD -i "$internal_interface" -o "$output_interface" -j ACCEPT
iptables -A FORWARD -i "$output_interface" -o "$internal_interface" \
  -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT
iptables -t nat -A POSTROUTING -s "$WSLAN_INTERNAL_CIDR" -o "$output_interface" -j MASQUERADE

# shellcheck disable=SC2086
dnsmasq --keep-in-foreground --bind-interfaces --listen-address="$internal_address" \
  --cache-size=1000 $dns_arguments &
dns_pid=$!
trap 'kill "$dns_pid" 2>/dev/null || true' EXIT HUP INT TERM

exec /usr/local/bin/wslan
