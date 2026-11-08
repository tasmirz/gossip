#!/usr/bin/env bash
set -euo pipefail

# Supplemental network-boundary smoke test. This uses two isolated Docker
# network namespaces; it does not replace docs/two-host-acceptance.md's
# separate-machine LAN/FRP acceptance.

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$ROOT"
for dependency in docker openssl python3 go; do
	command -v "$dependency" >/dev/null || { printf 'Missing dependency: %s\n' "$dependency" >&2; exit 1; }
done

suffix=$$
network="gossip-acceptance-$suffix"
home_name="gossip-acceptance-home-$suffix"
data=$(mktemp -d "${TMPDIR:-/tmp}/gossip-twohost.XXXXXX")
chmod 700 "$data"
cleanup() {
	docker rm -f "$home_name" >/dev/null 2>&1 || true
	docker network rm "$network" >/dev/null 2>&1 || true
	rm -rf -- "$data"
}
trap cleanup EXIT

CGO_ENABLED=0 go build -trimpath -o "$data/gossip" ./cmd/gossip
chmod 700 "$data/gossip"
docker network create "$network" >/dev/null

openssl req -x509 -newkey rsa:2048 -nodes -keyout "$data/ca.key" -out "$data/ca.crt" \
	-days 3 -subj '/CN=Gossip acceptance CA' \
	-addext 'basicConstraints=critical,CA:TRUE' \
	-addext 'keyUsage=critical,keyCertSign,cRLSign' >/dev/null 2>&1

make_cert() {
	local name=$1 usage=$2
	openssl req -newkey rsa:2048 -nodes -keyout "$data/$name.key" -out "$data/$name.csr" \
		-subj "/CN=$name" >/dev/null 2>&1
	if [[ "$usage" == server ]]; then
		printf 'subjectAltName=DNS:home\nextendedKeyUsage=serverAuth\nkeyUsage=digitalSignature,keyEncipherment\n' > "$data/$name.ext"
	else
		printf 'extendedKeyUsage=clientAuth\nkeyUsage=digitalSignature,keyEncipherment\n' > "$data/$name.ext"
	fi
	openssl x509 -req -in "$data/$name.csr" -CA "$data/ca.crt" -CAkey "$data/ca.key" \
		-CAcreateserial -out "$data/$name.crt" -days 2 -sha256 -extfile "$data/$name.ext" >/dev/null 2>&1
}
make_cert server server
make_cert owner client
make_cert worker client
make_cert ungranted client

"$data/gossip" coord-init --db "$data/home.db" --name acceptance-home --owner owner --credential-file "$data/owner.json" >/dev/null
project=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["project_id"])' "$data/owner.json")
"$data/gossip" coord-access --db "$data/home.db" --credential-file "$data/owner.json" --action grant --certificate "$data/owner.crt" >/dev/null
"$data/gossip" coord-add --db "$data/home.db" --owner-credential "$data/owner.json" --name worker --role worker \
	--client-certificate "$data/worker.crt" --credential-file "$data/worker.json" >/dev/null
"$data/gossip" coord-init --db "$data/home.db" --name acceptance-other --owner other --credential-file "$data/other-owner.json" >/dev/null
other_project=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["project_id"])' "$data/other-owner.json")
"$data/gossip" coord-access --db "$data/home.db" --credential-file "$data/other-owner.json" --action grant --certificate "$data/worker.crt" >/dev/null

cat > "$data/request.py" <<'PY'
import json, ssl, sys, urllib.error, urllib.request
project, credential_file, cert_file, key_file, expected = sys.argv[1:]
with open(credential_file, encoding="utf-8") as source:
    credential = json.load(source)
context = ssl.create_default_context(cafile="/data/ca.crt")
context.load_cert_chain(certfile=cert_file, keyfile=key_file)
request = urllib.request.Request(
    "https://home:8443/v1/call",
    data=json.dumps({"operation": "identity.read", "project": project}).encode(),
    headers={"Authorization": "Bearer " + credential["token"], "Content-Type": "application/json"},
    method="POST",
)
opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), urllib.request.HTTPSHandler(context=context))
try:
    response = opener.open(request, timeout=5)
    actual = response.status
    response.close()
except urllib.error.HTTPError as error:
    actual = error.code
    error.read()
if actual != int(expected):
    raise SystemExit(f"expected HTTP {expected}, got {actual}")
print(f"HTTP {actual} as expected for {project}")
PY

docker run -d --name "$home_name" --network "$network" --network-alias home -v "$data:/data" alpine:latest \
	/data/gossip coord-serve --db /data/home.db --listen 0.0.0.0:8443 --tls-cert /data/server.crt \
	--tls-key /data/server.key --client-ca /data/ca.crt >/dev/null

run_request() {
	docker run --rm --network "$network" -v "$data:/data:ro" python:3.12-slim \
		python /data/request.py "$@"
}

ready=false
for attempt in $(seq 1 30); do
	if run_request "$project" /data/owner.json /data/owner.crt /data/owner.key 503 >/dev/null 2>&1; then
		ready=true
		break
	fi
	sleep 1
done
if [[ "$ready" != true ]]; then
	printf 'Coordinator did not become ready on the isolated network.\n' >&2
	exit 1
fi

"$data/gossip" coord-remote --db "$data/home.db" --action status
run_request "$project" /data/owner.json /data/owner.crt /data/owner.key 503
"$data/gossip" coord-remote --db "$data/home.db" --action enable
run_request "$project" /data/owner.json /data/owner.crt /data/owner.key 200
run_request "$project" /data/worker.json /data/worker.crt /data/worker.key 200
run_request "$project" /data/owner.json /data/ungranted.crt /data/ungranted.key 403
run_request "$other_project" /data/worker.json /data/worker.crt /data/worker.key 401
"$data/gossip" coord-access --db "$data/home.db" --credential-file "$data/owner.json" --action revoke --certificate "$data/worker.crt" >/dev/null
run_request "$project" /data/worker.json /data/worker.crt /data/worker.key 403
"$data/gossip" coord-remote --db "$data/home.db" --action disable
run_request "$project" /data/owner.json /data/owner.crt /data/owner.key 503

printf 'Container-network smoke passed: mTLS, project certificates, project credentials, revocation, and the default-off kill switch.\n'
