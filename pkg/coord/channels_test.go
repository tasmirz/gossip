package coord_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/tasmirz/gossip/pkg/coord"
)

func TestGroupChannelsLifecycleAndOversight(t *testing.T) {
	s, _, p, owner, worker := setup(t)
	ctx := context.Background()

	reviewer, err := s.AddPrincipal(ctx, p.ID, owner.Token, "reviewer", "worker")
	if err != nil {
		t.Fatal(err)
	}
	agent3, err := s.AddPrincipal(ctx, p.ID, owner.Token, "helper", "worker")
	if err != nil {
		t.Fatal(err)
	}
	outsider, err := s.AddPrincipal(ctx, p.ID, owner.Token, "outsider", "worker")
	if err != nil {
		t.Fatal(err)
	}

	// Worker creates a suffixed group chat and announces it in # general.
	ch, err := s.CreateChannel(ctx, p.ID, worker.Token, "api-sync", "Coordinate schema changes", nil, true, true)
	if err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}
	if !strings.HasPrefix(ch.Name, "api-sync-") || ch.MyStatus != "joined" {
		t.Fatalf("unexpected channel created: %+v", ch)
	}

	generalMsgs, err := s.ListMessages(ctx, p.ID, outsider.Token, 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(generalMsgs) == 0 || !strings.Contains(generalMsgs[len(generalMsgs)-1].Body, "#"+ch.Name) {
		t.Fatalf("expected # general announcement for %s, got %+v", ch.Name, generalMsgs)
	}

	// Non-member reviewer sees the channel metadata to ask to join, but cannot read or send.
	chans, err := s.ListChannels(ctx, p.ID, reviewer.Token)
	if err != nil || len(chans) != 1 || chans[0].MyStatus != "none" {
		t.Fatalf("unexpected reviewer ListChannels: chans=%+v err=%v", chans, err)
	}
	if _, err := s.ChannelMessages(ctx, p.ID, reviewer.Token, ch.ID, 0, 0, 20, false); !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("non-member read expected ErrUnauthorized, got %v", err)
	}
	if _, err := s.SendMessageWithID(ctx, p.ID, worker.Token, reviewer.PrincipalID, "channel:"+ch.ID, "", "should fail", "req-bad-dm-channel"); err == nil {
		t.Fatal("expected recipient+channel combination to be rejected")
	}

	// Worker posts inside the group chat.
	secretNote := "GROUP-SECRET-SYNC-MESSAGE"
	if _, err := s.SendMessageWithID(ctx, p.ID, worker.Token, "", "channel:"+ch.ID, "", secretNote, "req-group-1"); err != nil {
		t.Fatalf("worker send to group chat: %v", err)
	}

	// Non-member reviewer tries direct message send on group channel -> unauthorized.
	if _, err := s.SendMessageWithID(ctx, p.ID, reviewer.Token, "", "channel:"+ch.ID, "", "intruder", "req-unauth-send"); !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("non-member send expected ErrUnauthorized, got %v", err)
	}

	// Reviewer asks to join (announced in # general), then worker approves.
	reqCh, err := s.RequestJoinChannel(ctx, p.ID, reviewer.Token, ch.ID, "Reviewing schema", true)
	if err != nil || reqCh.MyStatus != "requested" {
		t.Fatalf("RequestJoinChannel: ch=%+v err=%v", reqCh, err)
	}
	approvedCh, err := s.RespondChannelMembership(ctx, p.ID, worker.Token, ch.ID, reviewer.PrincipalID, "approve")
	if err != nil || approvedCh.MemberCount < 2 {
		t.Fatalf("approve join: ch=%+v err=%v", approvedCh, err)
	}

	// Reviewer can now read & reply inside the group chat.
	if msgs, err := s.ChannelMessages(ctx, p.ID, reviewer.Token, ch.ID, 0, 0, 20, false); err != nil || len(msgs) != 1 || msgs[0].Body != secretNote {
		t.Fatalf("approved reviewer read failed: msgs=%+v err=%v", msgs, err)
	}
	if _, err := s.SendMessageWithID(ctx, p.ID, reviewer.Token, "", "channel:"+ch.ID, "", "Reviewer joined ok", "req-group-2"); err != nil {
		t.Fatalf("reviewer group send failed: %v", err)
	}

	// Owner invites agent3 via # general, then agent3 accepts ("join").
	if _, err := s.InviteToChannel(ctx, p.ID, owner.Token, ch.ID, agent3.PrincipalID, "Join api-sync", true); err != nil {
		t.Fatalf("InviteToChannel: %v", err)
	}
	if _, err := s.RespondChannelMembership(ctx, p.ID, agent3.Token, ch.ID, "", "join"); err != nil {
		t.Fatalf("agent3 accept invite: %v", err)
	}

	// Owner can inspect & verify all group chat messages without joining as a member.
	ownerMsgs, err := s.ChannelMessages(ctx, p.ID, owner.Token, ch.ID, 0, 0, 20, false)
	if err != nil || len(ownerMsgs) != 2 {
		t.Fatalf("owner oversight expected 2 messages, got %+v err=%v", ownerMsgs, err)
	}

	// Outsider cannot see group chat messages via ListMessages, RecentMessages, or SearchProject.
	outsiderList, err := s.ListMessages(ctx, p.ID, outsider.Token, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range outsiderList {
		if strings.Contains(m.Body, secretNote) {
			t.Fatalf("outsider ListMessages leaked group chat secret: %+v", m)
		}
	}
	outsiderRecent, err := s.RecentMessages(ctx, p.ID, outsider.Token, 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range outsiderRecent {
		if strings.Contains(m.Body, secretNote) {
			t.Fatalf("outsider RecentMessages leaked group chat secret: %+v", m)
		}
	}
	searchRes, err := s.SearchProject(ctx, p.ID, outsider.Token, "GROUP-SECRET")
	if err != nil {
		t.Fatal(err)
	}
	if len(searchRes.Messages) != 0 {
		t.Fatalf("outsider SearchProject leaked group chat message: %+v", searchRes.Messages)
	}
	ownerSearch, err := s.SearchProject(ctx, p.ID, owner.Token, "GROUP-SECRET")
	if err != nil || len(ownerSearch.Messages) != 1 {
		t.Fatalf("owner SearchProject should verify group chat message, got %+v err=%v", ownerSearch.Messages, err)
	}

	// 1-to-1 DMs remain intact and private between sender & recipient.
	dmSecret := "PRIVATE-1ON1-DM-TOKEN"
	if _, err := s.SendMessageWithID(ctx, p.ID, worker.Token, reviewer.PrincipalID, "", "", dmSecret, "req-dm-1"); err != nil {
		t.Fatalf("1-on-1 DM send failed: %v", err)
	}
	dmConv, err := s.ConversationMessages(ctx, p.ID, reviewer.Token, worker.PrincipalID, 0, 0, 20, false)
	if err != nil || len(dmConv) != 1 || dmConv[0].Body != dmSecret {
		t.Fatalf("reviewer 1-on-1 DM read failed: msgs=%+v err=%v", dmConv, err)
	}
	for _, viewer := range []struct {
		token     string
		group, dm bool
	}{
		{owner.Token, true, false},
		{reviewer.Token, true, true},
		{outsider.Token, false, false},
	} {
		activity, err := s.ConversationActivity(ctx, p.ID, viewer.token)
		if err != nil {
			t.Fatal(err)
		}
		rooms := map[string]bool{}
		for _, item := range activity {
			rooms[item.Room] = true
		}
		if rooms["channel:"+ch.ID] != viewer.group || rooms["dm:"+worker.PrincipalID] != viewer.dm {
			t.Fatalf("activity visibility: %+v", activity)
		}
	}
	agent3Msgs, err := s.ListMessages(ctx, p.ID, agent3.Token, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range agent3Msgs {
		if strings.Contains(m.Body, dmSecret) {
			t.Fatalf("third agent saw 1-on-1 DM: %+v", m)
		}
	}
}

func TestGroupChannelsHTTPAndMCP(t *testing.T) {
	s, _, p, owner, worker := setup(t)
	ctx := context.Background()
	peer, err := s.AddPrincipal(ctx, p.ID, owner.Token, "subagent-peer", "worker")
	if err != nil {
		t.Fatal(err)
	}

	h := coord.NewWebHandler(s)
	doReq := func(cookie *http.Cookie, method, path string, body any) *httptest.ResponseRecorder {
		t.Helper()
		var data []byte
		if body != nil {
			data, _ = json.Marshal(body)
		}
		req := httptest.NewRequest(method, "http://localhost:8081"+path, bytes.NewReader(data))
		req.RemoteAddr = "127.0.0.1:22222"
		req.Header.Set("Origin", "http://localhost:8081")
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if cookie != nil {
			req.AddCookie(cookie)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	loginCookie := func(tok string) *http.Cookie {
		t.Helper()
		rec := doReq(nil, "POST", "/api/login", map[string]string{"project_id": p.ID, "token": tok})
		if rec.Code != http.StatusOK || len(rec.Result().Cookies()) == 0 {
			t.Fatalf("login failed: %d %s", rec.Code, rec.Body.String())
		}
		return rec.Result().Cookies()[0]
	}
	ownerCookie := loginCookie(owner.Token)
	workerCookie := loginCookie(worker.Token)
	peerCookie := loginCookie(peer.Token)

	// Create group chat via HTTP as worker with auto_suffix=false.
	rec := doReq(workerCookie, "POST", "/api/projects/"+p.ID+"/channels", map[string]any{
		"operation":   "create",
		"name":        "codex-subagent",
		"topic":       "Subagent status sync",
		"auto_suffix": false,
		"announce":    true,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("HTTP create channel failed: %d %s", rec.Code, rec.Body.String())
	}
	var ch coord.ProjectChannel
	if err := json.Unmarshal(rec.Body.Bytes(), &ch); err != nil {
		t.Fatal(err)
	}

	// Post via HTTP messages API with channel_id.
	rec = doReq(workerCookie, "POST", "/api/projects/"+p.ID+"/messages", map[string]any{
		"channel_id": ch.ID,
		"body":       "Hello from HTTP channel post",
		"request_id": "http-ch-1",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("HTTP send channel message failed: %d %s", rec.Code, rec.Body.String())
	}

	// Peer asks to join and gets approved via MCP gossip_channel tool.
	peerMCP := coord.MCPServer(s, p.ID, peer.Token)
	workerMCP := coord.MCPServer(s, p.ID, worker.Token)

	askTool := peerMCP.GetTool("gossip_channel")
	if askTool == nil {
		t.Fatal("missing gossip_channel MCP tool")
	}
	res, err := askTool.Handler(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{
		Name:      "gossip_channel",
		Arguments: map[string]any{"operation": "ask_join", "channel_id": ch.ID, "body": "Peer requesting access", "announce": true},
	}})
	if err != nil || res.IsError {
		t.Fatalf("MCP ask_join failed: %v %+v", err, res)
	}

	approveTool := workerMCP.GetTool("gossip_channel")
	res, err = approveTool.Handler(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{
		Name:      "gossip_channel",
		Arguments: map[string]any{"operation": "approve", "channel_id": ch.ID, "principal_id": peer.PrincipalID},
	}})
	if err != nil || res.IsError {
		t.Fatalf("MCP approve failed: %v %+v", err, res)
	}

	// Peer sends to the channel via gossip_send(channel_id=...) and reads via HTTP ?channel=.
	sendTool := peerMCP.GetTool("gossip_send")
	res, err = sendTool.Handler(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{
		Name:      "gossip_send",
		Arguments: map[string]any{"channel_id": ch.ID, "body": "Peer joined via MCP", "request_id": "mcp-ch-1"},
	}})
	if err != nil || res.IsError {
		t.Fatalf("MCP gossip_send to channel failed: %v %+v", err, res)
	}

	// Owner verifies via HTTP GET /messages?channel=<id>.
	rec = doReq(ownerCookie, "GET", "/api/projects/"+p.ID+"/messages?channel="+ch.ID, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("HTTP owner channel messages failed: %d %s", rec.Code, rec.Body.String())
	}
	var msgs []coord.Message
	if err := json.Unmarshal(rec.Body.Bytes(), &msgs); err != nil || len(msgs) != 2 {
		t.Fatalf("expected 2 channel messages, got %d (%s) err=%v", len(msgs), rec.Body.String(), err)
	}

	// Also verify 1-on-1 DM over HTTP continues to work cleanly.
	rec = doReq(workerCookie, "POST", "/api/projects/"+p.ID+"/messages", map[string]any{
		"recipient":  peer.PrincipalID,
		"body":       "Direct ping over HTTP",
		"request_id": "http-dm-1",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("HTTP 1-on-1 DM failed: %d %s", rec.Code, rec.Body.String())
	}
	rec = doReq(peerCookie, "GET", "/api/projects/"+p.ID+"/messages?recipient="+worker.PrincipalID, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("HTTP 1-on-1 DM read failed: %d %s", rec.Code, rec.Body.String())
	}
	var dmMsgs []coord.Message
	if err := json.Unmarshal(rec.Body.Bytes(), &dmMsgs); err != nil || len(dmMsgs) != 1 || dmMsgs[0].Body != "Direct ping over HTTP" {
		t.Fatalf("expected 1 DM message, got %+v err=%v", dmMsgs, err)
	}
}

func TestChannelMessagesLastReadHashReturnsOnePrevPlusNew(t *testing.T) {
	s, _, p, owner, worker := setup(t)
	ctx := context.Background()

	ch, err := s.CreateChannel(ctx, p.ID, owner.Token, "impl", "Implementation room", []string{worker.PrincipalID}, false, false)
	if err != nil {
		t.Fatal(err)
	}
	generalMsg, err := s.SendMessageWithID(ctx, p.ID, owner.Token, "", "", "", "General noise", "ch-gen-1")
	if err != nil {
		t.Fatal(err)
	}
	c1, err := s.SendMessageWithID(ctx, p.ID, owner.Token, "", "channel:"+ch.ID, "", "Channel 1", "ch-m-1")
	if err != nil {
		t.Fatal(err)
	}
	c2, err := s.SendMessageWithID(ctx, p.ID, worker.Token, "", "channel:"+ch.ID, "", "Channel 2", "ch-m-2")
	if err != nil {
		t.Fatal(err)
	}
	c3, err := s.SendMessageWithID(ctx, p.ID, owner.Token, "", "channel:"+ch.ID, "", "Channel 3", "ch-m-3")
	if err != nil {
		t.Fatal(err)
	}

	workerMCP := coord.MCPServer(s, p.ID, worker.Token)
	chTool := workerMCP.GetTool("gossip_channel")

	// Omitting last_read_hash returns full recent channel messages [c1, c2, c3].
	res, err := chTool.Handler(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{
		Name:      "gossip_channel",
		Arguments: map[string]any{"operation": "messages", "channel_id": ch.ID},
	}})
	if err != nil || res.IsError {
		t.Fatalf("channel read all: err=%v res=%+v", err, res)
	}
	var all []coord.Message
	if err := json.Unmarshal([]byte(res.Content[0].(mcp.TextContent).Text), &all); err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 || all[0].Hash != c1.Hash || all[2].Hash != c3.Hash {
		t.Fatalf("expected 3 channel messages, got %+v", all)
	}

	// Passing last_read_hash=c2.Hash returns [c2, c3] (1 prev + new ones).
	res, err = chTool.Handler(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{
		Name:      "gossip_channel",
		Arguments: map[string]any{"operation": "messages", "channel_id": ch.ID, "last_read_hash": c2.Hash},
	}})
	if err != nil || res.IsError {
		t.Fatalf("channel read from hash c2: err=%v res=%+v", err, res)
	}
	var delta []coord.Message
	_ = json.Unmarshal([]byte(res.Content[0].(mcp.TextContent).Text), &delta)
	if len(delta) != 2 || delta[0].ID != c2.ID || delta[1].ID != c3.ID {
		t.Fatalf("expected [c2, c3] (1 prev + new), got %+v", delta)
	}

	// Hash from #general must not resolve inside channel scope.
	res, err = chTool.Handler(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{
		Name:      "gossip_channel",
		Arguments: map[string]any{"operation": "messages", "channel_id": ch.ID, "last_read_hash": generalMsg.Hash},
	}})
	if err != nil || !res.IsError {
		t.Fatalf("expected error for non-channel hash, got res=%+v err=%v", res, err)
	}
}
