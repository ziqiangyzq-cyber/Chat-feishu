package wecom

import (
	"context"
	"errors"
	"log"
	"strings"
	"time"
)

const (
	// streamReqIDSafetyTTL stays below WeCom's approximately six-minute
	// callback req_id lifetime. Once crossed, the current cumulative content is
	// delivered with a standalone aibot_send_msg instead of another stream
	// update.
	streamReqIDSafetyTTL   = 5 * time.Minute
	maxTrackedStreamFrames = 512
)

// chatStream tracks the two identities shared by every frame in one stream:
// ID is body.stream.id and ReqID is the callback (or proactive) headers.req_id.
type chatStream struct {
	ID           string
	ReqID        string
	Reply        bool
	Started      bool
	StartedAt    time.Time
	LastText     string
	Fallback     bool
	FallbackSent bool
}

// streamFrameInfo captures what a stream frame carried so rejected content can
// be recovered through a standalone message.
type streamFrameInfo struct {
	ChatID   string
	StreamID string
	Content  string
	Finish   bool
	SentAt   time.Time
}

// streamMarkdown writes (or updates) a streaming markdown message for chatID.
// Successive calls with finish=false share one stream id; finish=true ends it.
func (c *Client) streamMarkdown(ctx context.Context, chatID, content string, finish bool) error {
	return c.streamMarkdownReply(ctx, chatID, "", time.Time{}, content, finish)
}

// streamMarkdownReply starts a callback-bound stream when responseReqID is
// present. Pure proactive streams leave it empty and get one generated req_id
// that remains stable for their entire lifetime.
func (c *Client) streamMarkdownReply(
	ctx context.Context,
	chatID, responseReqID string,
	responseReqBoundAt time.Time,
	content string,
	finish bool,
) error {
	chatID = strings.TrimSpace(chatID)
	responseReqID = strings.TrimSpace(responseReqID)
	content = strings.TrimSpace(content)
	if chatID == "" {
		return errors.New("wecom: stream requires chatID")
	}
	if content == "" && !finish {
		return nil
	}

	c.streamMu.Lock()
	st := c.streams[chatID]
	if st == nil {
		startedAt := time.Now()
		if responseReqID != "" && !responseReqBoundAt.IsZero() {
			startedAt = responseReqBoundAt
		}
		reqID := responseReqID
		if reqID == "" {
			reqID = newReqID("stream")
		}
		st = &chatStream{
			ID:        newReqID("stream"),
			ReqID:     reqID,
			Reply:     responseReqID != "",
			StartedAt: startedAt,
		}
		c.streams[chatID] = st
	}
	if content != "" {
		st.LastText = content
	}
	if !st.Fallback && time.Since(st.StartedAt) >= streamReqIDSafetyTTL {
		// A callback req_id may already be stale before the first renderer
		// output, or an open stream may have crossed the safety line.
		st.Fallback = true
	}
	streamID := st.ID
	reqID := st.ReqID
	reply := st.Reply
	started := st.Started
	fallback := st.Fallback
	fallbackText := st.LastText
	sendFallback := fallback && (!st.FallbackSent || finish)
	if sendFallback {
		st.FallbackSent = true
	}
	if !started && !fallback {
		st.Started = true
	}
	if finish {
		delete(c.streams, chatID)
	}
	c.streamMu.Unlock()

	if fallback {
		if !sendFallback || fallbackText == "" {
			return nil
		}
		return c.sendFrame(ctx, chatID, markdownFrame(fallbackText))
	}

	trackedContent := content
	if trackedContent == "" {
		trackedContent = fallbackText
	}
	info := streamFrameInfo{
		ChatID:   chatID,
		StreamID: streamID,
		Content:  trackedContent,
		Finish:   finish,
		SentAt:   time.Now(),
	}
	// Register before writing so a fast gateway response cannot race ahead of
	// correlation state on the read loop.
	c.trackStreamFrame(reqID, info)

	var err error
	if !started {
		var wire respondMsgFrame
		if reply {
			wire = newRespondMsgFrame(reqID, markdownFrame(content))
		} else {
			wire = newSendMsgFrame(chatID, markdownFrame(content))
			wire.Headers.ReqID = reqID
		}
		wire.Body.Stream = &streamMeta{ID: streamID, Finish: finish, Content: content}
		err = c.writeJSON(ctx, wire)
	} else {
		wire := newStreamUpdateFrame(reqID, chatID, streamID, content, finish)
		err = c.writeJSON(ctx, wire)
	}
	if err != nil {
		c.untrackStreamFrame(reqID, info)
		if !started {
			c.streamMu.Lock()
			if current := c.streams[chatID]; current != nil && current.ID == streamID {
				delete(c.streams, chatID)
			}
			c.streamMu.Unlock()
		}
	}
	return err
}

// trackStreamFrame records an in-flight stream frame keyed by req_id, pruning
// stale entries so an upstream that stops answering cannot grow the map.
func (c *Client) trackStreamFrame(reqID string, info streamFrameInfo) {
	if reqID == "" {
		return
	}
	c.streamMu.Lock()
	trackedCount := 0
	for _, queue := range c.streamFrames {
		trackedCount += len(queue)
	}
	if trackedCount >= maxTrackedStreamFrames {
		cutoff := time.Now().Add(-2 * time.Minute)
		for id, queue := range c.streamFrames {
			keep := queue[:0]
			for _, fi := range queue {
				if !fi.SentAt.Before(cutoff) {
					keep = append(keep, fi)
				}
			}
			if len(keep) == 0 {
				delete(c.streamFrames, id)
			} else {
				c.streamFrames[id] = keep
			}
		}
	}
	c.streamFrames[reqID] = append(c.streamFrames[reqID], info)
	c.streamMu.Unlock()
}

// untrackStreamFrame removes a frame whose socket write failed. Search from the
// tail because it is normally the item most recently registered under reqID.
func (c *Client) untrackStreamFrame(reqID string, info streamFrameInfo) {
	c.streamMu.Lock()
	defer c.streamMu.Unlock()
	queue := c.streamFrames[reqID]
	for i := len(queue) - 1; i >= 0; i-- {
		if queue[i].StreamID != info.StreamID || queue[i].Content != info.Content || queue[i].Finish != info.Finish {
			continue
		}
		queue = append(queue[:i], queue[i+1:]...)
		if len(queue) == 0 {
			delete(c.streamFrames, reqID)
		} else {
			c.streamFrames[reqID] = queue
		}
		return
	}
}

// handleStreamFrameResponse consumes gateway responses to tracked stream
// frames. A success clears the next queued record. A rejection (notably 846605)
// switches the stream to standalone fallback instead of trying another stream
// req_id. Returns true when the envelope belonged to a stream frame.
func (c *Client) handleStreamFrameResponse(env frameEnvelope) bool {
	reqID := env.Headers.ReqID
	if reqID == "" {
		return false
	}
	c.streamMu.Lock()
	queue := c.streamFrames[reqID]
	tracked := len(queue) > 0
	var info streamFrameInfo
	if tracked {
		info = queue[0]
		queue = queue[1:]
		if len(queue) == 0 {
			delete(c.streamFrames, reqID)
		} else {
			c.streamFrames[reqID] = queue
		}
	}
	fallback := false
	if tracked && env.ErrCode != 0 {
		st := c.streams[info.ChatID]
		switch {
		case st != nil && st.ID == info.StreamID:
			// The stable req_id was rejected, so this stream must never emit
			// another update. Preserve state until finish so the final cumulative
			// content also lands through aibot_send_msg.
			st.Fallback = true
			if !st.FallbackSent {
				st.FallbackSent = true
				fallback = true
			}
		case st == nil && info.Finish:
			// The finish frame failed after local state was already cleared;
			// the final answer must still reach the user.
			fallback = true
		case st != nil && st.ID != info.StreamID && info.Finish:
			// A newer stream is open; do not let the stale rejection tear it
			// down, but still preserve the rejected final content.
			fallback = true
		}
	}
	c.streamMu.Unlock()
	if !tracked {
		return false
	}
	if env.ErrCode == 0 {
		return true
	}
	if !fallback {
		log.Printf("wecom: stream frame rejected errcode=%d chat=%s req=%s (superseded, no resend)", env.ErrCode, info.ChatID, reqID)
		return true
	}
	log.Printf("wecom: stream frame rejected errcode=%d chat=%s finish=%v; falling back to standalone message", env.ErrCode, info.ChatID, info.Finish)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := c.sendFrame(ctx, info.ChatID, markdownFrame(info.Content)); err != nil {
			log.Printf("wecom: standalone fallback failed chat=%s: %v", info.ChatID, err)
		}
	}()
	return true
}

func newStreamUpdateFrame(reqID, chatID, streamID, content string, finish bool) respondUpdateMsgFrame {
	wire := respondUpdateMsgFrame{
		Cmd:     frameCmdRespondUpdateMsg,
		Headers: frameHeaders{ReqID: reqID},
	}
	wire.Body.ChatID = chatID
	wire.Body.MsgType = "markdown"
	if content != "" {
		wire.Body.Text = &textBody{Content: content}
	}
	wire.Body.Stream = &streamMeta{ID: streamID, Finish: finish, Content: content}
	return wire
}

// dropStream forgets any open stream for chatID without sending a finish frame.
func (c *Client) dropStream(chatID string) {
	c.streamMu.Lock()
	delete(c.streams, chatID)
	c.streamMu.Unlock()
}

// streamAge returns how long the open stream for chatID has been active.
func (c *Client) streamAge(chatID string, now time.Time) (time.Duration, bool) {
	c.streamMu.Lock()
	defer c.streamMu.Unlock()
	st := c.streams[chatID]
	if st == nil || (!st.Started && !st.Fallback) {
		return 0, false
	}
	return now.Sub(st.StartedAt), true
}

// activeStreamChats returns chat ids with an open stream older than maxAge.
func (c *Client) activeStreamChats(now time.Time, maxAge time.Duration) []string {
	if maxAge <= 0 {
		return nil
	}
	c.streamMu.Lock()
	defer c.streamMu.Unlock()
	out := make([]string, 0)
	for chatID, st := range c.streams {
		if st != nil && (st.Started || st.Fallback) && now.Sub(st.StartedAt) >= maxAge {
			out = append(out, chatID)
		}
	}
	return out
}
