# Two-host acceptance runbook

This procedure closes the P4 acceptance gate for a real LAN or FRP deployment. Run Host A and Host B on separate physical computers. Docker namespaces or two processes on one computer are useful tests, but do not count as this acceptance run. Use a disposable project and test certificates. Never paste private keys or project credential tokens into this record.

## Prerequisites

- The same built `gossip` version is installed on both hosts.
- Host A can listen on the LAN, or an FRP TCP route forwards to its TLS port. Host B can reach that address.
- A private CA has issued the home server certificate, an owner client certificate, a worker client certificate, and a valid but ungranted test client certificate. Each certificate has the appropriate server/client EKU; the home certificate SAN matches the hostname Host B uses.
- Host A has the server key and its owner client certificate/key. Host B has its worker certificate/key. The owner receives the worker and test client public certificates. Transfer public certificates and the worker project credential over a separate secure channel. Keep private keys on their respective hosts.
- On Windows, apply equivalent user-only ACLs to private keys and credentials. On Unix, keep them mode 0600. Record Gossip version, OS, host labels, route type (LAN or FRP), and date; keep actual private addresses out of a public report if needed.

## Set up the single home

On Host A, create a private test directory and initialize a disposable project in the database used by the coordinator. Keep the server and dashboard running in separate terminals.

```sh
mkdir -p /private/gossip && chmod 700 /private/gossip
gossip coord-init --db /private/gossip/home.db --name two-host-check --owner acceptance-owner --credential-file /private/gossip/owner.json
gossip coord-access --db /private/gossip/home.db --credential-file /private/gossip/owner.json --action grant --certificate /private/gossip/owner-client.crt
```

Start the authoritative home listener. Choose the LAN address or `0.0.0.0` only when the host firewall restricts access to the intended network. The server certificate must match the URL Host B will use. Keep the owner credential on Host A and start its loopback dashboard: `gossip coord-ui --db /private/gossip/home.db --port 8081`.

```sh
gossip coord-serve --db /private/gossip/home.db --listen 0.0.0.0:8443 --tls-cert /private/gossip/home.crt --tls-key /private/gossip/home.key --client-ca /private/gossip/client-ca.crt
```

If using FRP, forward TCP to this TLS listener. Do not expose the legacy `gossip ui` dashboard through the tunnel.

## Enroll the remote worker

On Host A, create the worker membership and grant its client certificate to this project atomically. The command writes the worker credential locally with restrictive permissions; transfer that file securely to Host B.

```sh
gossip coord-add --remote https://HOME_HOST:8443 --tls-ca /private/gossip/server-ca.crt --tls-cert /private/gossip/owner-client.crt --tls-key /private/gossip/owner-client.key --owner-credential /private/gossip/owner.json --name acceptance-worker --role worker --client-certificate /private/gossip/worker-client.crt --credential-file /private/gossip/worker.json
```

On Host B, protect the received credential and configure the coding-agent MCP host to start its project adapter with its working directory set to the registered checkout when testing source capture. (The adapter uses stdio, so the coding-agent MCP host should launch it.)

```sh
chmod 600 /private/gossip/worker.json
gossip coord-mcp --remote https://HOME_HOST:8443 --tls-ca /private/gossip/server-ca.crt --tls-cert /private/gossip/worker-client.crt --tls-key /private/gossip/worker-client.key --credential-file /private/gossip/worker.json
```

## Exercise access control and collaboration

1. In the owner dashboard on Host A, connect the owner credential, create a repository under Workspaces, then create a plan and a research or implementation task. During planning, select either **Full code review** or **Markdown report with UML class and sequence diagrams**. The MCP task tool must ask the user to choose when the request did not specify a review type.
2. In the worker's coding-agent host on Host B, connect the worker MCP adapter. Confirm the owner sees an authenticated heartbeat for that project. Register the Host B checkout as a worker-owned workspace; verify the owner can see its name and revision but not its absolute local path.
3. Have the worker acknowledge the plan, claim the task, exchange a project message, and submit the planned review. For full-code review, capture the candidate from the registered checkout first. For diagram review, submit the required Markdown sections and both UML diagrams. Confirm the owner receives the review event, reads the exact evidence, and accepts or requests rework.
4. Create a second disposable project on Host A and grant the worker certificate to that project without enrolling the worker there:

   ```sh
   gossip coord-init --db /private/gossip/home.db --name unrelated-check --owner unrelated-owner --credential-file /private/gossip/unrelated-owner.json
   gossip coord-access --db /private/gossip/home.db --credential-file /private/gossip/unrelated-owner.json --action grant --certificate /private/gossip/worker-client.crt
   ```

   From Host B, use the protected worker credential with the second project ID in a request to `/v1/call` (`identity.read`); the certificate gate should pass and the project credential should be rejected with 401. Keep the token out of command arguments and logs with a private temporary curl config:

   ```sh
   umask 077
   auth_cfg=$(mktemp)
   trap 'rm -f "$auth_cfg"' EXIT
   python3 - /private/gossip/worker.json > "$auth_cfg" <<'PY'
   import json, sys
   with open(sys.argv[1], encoding="utf-8") as source:
       credential = json.load(source)
   print("header = " + json.dumps("Authorization: Bearer " + credential["token"]))
   PY
   curl --config "$auth_cfg" --cacert /private/gossip/server-ca.crt \
     --cert /private/gossip/worker-client.crt --key /private/gossip/worker-client.key \
     -H 'Content-Type: application/json' \
     --data '{"operation":"identity.read","project":"SECOND_PROJECT_ID"}' \
     --write-out '\nHTTP %{http_code}\n' https://HOME_HOST:8443/v1/call
   rm -f "$auth_cfg"
   trap - EXIT
   ```

   Expect HTTP 401 and an unauthorized project credential result. Separately, use the valid-but-ungranted test client certificate with the enrolled worker credential against the first project; expect 403 from the certificate-grant gate. A certificate grant alone must never create project membership.
5. Revoke the worker membership from the owner dashboard. Verify the next authoritative project request is rejected, the worker cannot reconnect with the old credential, and its active task attempt is fenced. Re-enroll a fresh disposable worker credential, then revoke only the worker certificate grant and verify that requests are rejected again. Record the HTTP/MCP status and message for both cases.

## Exercise reconnect and restart

1. With the worker connected, stop Host A's coordinator. Confirm the worker cannot claim or renew a task while the home is unavailable. If the adapter has an outbox, queue one supported message or review request and confirm it is visibly pending rather than delivered.
2. Restart `coord-serve` on Host A against the same database. Restart the worker's `coord-mcp` adapter on Host B. Confirm presence returns after MCP initialization, queued work is delivered once, and a retry does not duplicate it.
3. Repeat one read and one write after the restart. Confirm the project ID and home database remain the same and an unrelated project's data is still inaccessible.

## Acceptance record

Record evidence with secret values removed:

| Evidence | Record |
| --- | --- |
| Date, Gossip version, Host A/Host B OS | |
| Route (LAN or FRP), server certificate name verified | |
| Project ID and plan revision | |
| Worker principal ID and certificate fingerprint suffix | |
| Review type and accepted report/candidate digest suffix | |
| Worker checkout name, branch, and commit (no absolute path) | |
| Wrong-project credential result | |
| Missing certificate grant result | |
| Membership revocation result and attempt fencing | |
| Certificate revocation result | |
| Home stop/start and worker reconnect observations | |
| Outbox retry and duplicate check | |

P4 is complete only after this procedure succeeds between separate physical hosts and the completed record is attached to the release review. The automated mutual-TLS and FRP tests remain useful prerequisites, not substitutes for this run.
