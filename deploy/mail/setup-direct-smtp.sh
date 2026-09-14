#!/usr/bin/env bash
set -euo pipefail

domain="${ZTAPI_MAIL_DOMAIN:-ztapi.vip}"
selector="${ZTAPI_DKIM_SELECTOR:-ztapi202609}"
public_ipv4="${ZTAPI_PUBLIC_IPV4:-}"
mail_host="mail.${domain}"
key_dir="/etc/opendkim/keys/${domain}"
record_dir="/etc/ztapi-mail"

if [[ "${EUID}" -ne 0 ]]; then
  echo "run as root" >&2
  exit 1
fi

if [[ ! "${public_ipv4}" =~ ^([0-9]{1,3}\.){3}[0-9]{1,3}$ ]]; then
  echo "set ZTAPI_PUBLIC_IPV4 to the server public IPv4 address" >&2
  exit 1
fi

docker_gateway="$(ip -4 -o addr show docker0 | awk '{print $4}' | cut -d/ -f1)"
if [[ -z "${docker_gateway}" ]]; then
  echo "docker0 IPv4 address was not found" >&2
  exit 1
fi
ztapi_egress_subnet="$(docker network inspect ztapi_egress --format '{{range .IPAM.Config}}{{.Subnet}}{{end}}' 2>/dev/null || true)"
if [[ -z "${ztapi_egress_subnet}" ]]; then
  echo "ZTAPI egress network was not found" >&2
  exit 1
fi

export DEBIAN_FRONTEND=noninteractive
echo "postfix postfix/mailname string ${domain}" | debconf-set-selections
echo "postfix postfix/main_mailer_type select Internet Site" | debconf-set-selections
apt-get update
apt-get install -y --no-install-recommends postfix opendkim opendkim-tools ca-certificates

install -d -m 0750 -o opendkim -g opendkim "${key_dir}"
install -d -m 0750 "${record_dir}"
if [[ ! -s "${key_dir}/${selector}.private" ]]; then
  opendkim-genkey -b 2048 -d "${domain}" -D "${key_dir}" -s "${selector}" -v
fi
chown -R opendkim:opendkim "${key_dir}"
chmod 0600 "${key_dir}/${selector}.private"

cat > /etc/opendkim.conf <<EOF
Syslog                  yes
SyslogSuccess           yes
Canonicalization        relaxed/simple
Mode                    sv
SubDomains              no
OversignHeaders         From
UserID                  opendkim
UMask                    007
Socket                   inet:8891@127.0.0.1
PidFile                  /run/opendkim/opendkim.pid
KeyTable                 refile:/etc/opendkim/key.table
SigningTable             refile:/etc/opendkim/signing.table
ExternalIgnoreList       refile:/etc/opendkim/trusted.hosts
InternalHosts            refile:/etc/opendkim/trusted.hosts
EOF

cat > /etc/opendkim/key.table <<EOF
${selector}._domainkey.${domain} ${domain}:${selector}:${key_dir}/${selector}.private
EOF
cat > /etc/opendkim/signing.table <<EOF
*@${domain} ${selector}._domainkey.${domain}
EOF
cat > /etc/opendkim/trusted.hosts <<EOF
127.0.0.1
localhost
${ztapi_egress_subnet}
EOF

postconf -e "myhostname = ${mail_host}"
postconf -e "mydomain = ${domain}"
postconf -e 'myorigin = $mydomain'
postconf -e 'mydestination = localhost'
postconf -e "inet_interfaces = 127.0.0.1, ${docker_gateway}"
postconf -e 'inet_protocols = ipv4'
postconf -e "mynetworks = 127.0.0.0/8, ${ztapi_egress_subnet}"
postconf -e 'smtpd_relay_restrictions = permit_mynetworks, reject_unauth_destination'
postconf -e 'smtpd_recipient_restrictions = permit_mynetworks, reject_unauth_destination'
# The relay is reachable only from the local Docker bridge. Do not advertise
# STARTTLS with the host's unrelated snake-oil certificate to application clients.
postconf -e 'smtpd_tls_security_level = none'
postconf -e 'smtp_tls_security_level = may'
postconf -e 'smtp_tls_CAfile = /etc/ssl/certs/ca-certificates.crt'
postconf -e 'milter_default_action = tempfail'
postconf -e 'milter_protocol = 6'
postconf -e 'smtpd_milters = inet:127.0.0.1:8891'
postconf -e 'non_smtpd_milters = inet:127.0.0.1:8891'
postconf -e 'disable_vrfy_command = yes'

systemctl enable opendkim postfix
systemctl restart opendkim
systemctl restart postfix
postfix check

dkim_value="$(sed -n 's/.*"\(.*\)".*/\1/p' "${key_dir}/${selector}.txt" | tr -d ' \t\r\n')"
cat > "${record_dir}/dns-records.txt" <<EOF
DNS records required before enabling ZTAPI email:

Type: A
Name: mail
Value: ${public_ipv4}
Proxy: DNS only

Type: TXT
Name: @
Value: v=spf1 a:${mail_host} ip4:${public_ipv4} -all

Type: TXT
Name: ${selector}._domainkey
Value: ${dkim_value}

Type: TXT
Name: _dmarc
Value: v=DMARC1; p=none; adkim=s; aspf=s

Provider action required:
Set the server IP reverse DNS (PTR) to ${mail_host}.
EOF

echo "Direct SMTP relay is listening only on loopback and ${docker_gateway}:25."
echo "DNS records: ${record_dir}/dns-records.txt"
