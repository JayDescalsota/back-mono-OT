package main

// This file holds the realtime chat stream: GET /sse/messages?userId=…
//
// How it works, in plain terms:
//  1. The browser opens this URL and KEEPS the connection open.
//  2. Every time someone sends a chat message anywhere, the messaging
//     service appends it to the "messaging:messages" Redis stream.
//  3. This loop waits on that stream (blocking read, 10s at a time) and
//     forwards each new message to the browser as "data: {...}" events.
//  4. Messages for threads the user is NOT in are skipped.
//  5. ?lastMessageId=… lets a reconnecting browser resume where it left
//     off instead of missing messages. "$" means "only new ones".
//
// The loop ends when the browser hangs up (r.Context().Done()).

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/redis/go-redis/v9"
)

// redisChatStream is the Redis stream every sent message is published to.
const redisChatStream = "messaging:messages"

func sseHandler(w http.ResponseWriter, r *http.Request, redisClient *redis.Client) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}

	userID := r.URL.Query().Get("userId")
	if userID == "" {
		http.Error(w, "userId query parameter required", http.StatusBadRequest)
		return
	}
	lastID := r.URL.Query().Get("lastMessageId")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	writer, ok := w.(io.Writer)
	if !ok {
		return
	}

	streamStart := "$"
	if lastID != "" {
		streamStart = lastID
	}

	for {
		// Stop looping the moment the browser disconnects.
		select {
		case <-r.Context().Done():
			return
		default:
		}

		// Wait up to 10 seconds for new messages. Block==0 would spin
		// the CPU; a long block means "wake me when mail arrives".
		args := &redis.XReadArgs{
			Streams: []string{redisChatStream, streamStart},
			Count:   20,
			Block:   10000,
		}
		streams, err := redisClient.XRead(r.Context(), args).Result()
		if err != nil {
			if err == redis.Nil {
				continue
			}
			return
		}

		for _, stream := range streams {
			for _, m := range stream.Messages {
				threadID, _ := m.Values["thread_id"].(string)
				senderID, _ := m.Values["sender_id"].(string)
				body, _ := m.Values["body"].(string)
				nonce, _ := m.Values["nonce"].(string)
				createdAt, _ := m.Values["created_at"].(string)
				participantsRaw, _ := m.Values["participants"].(string)

				// Only forward messages from threads this user belongs to.
				var participantIDs []string
				json.Unmarshal([]byte(participantsRaw), &participantIDs)
				found := false
				for _, pid := range participantIDs {
					if pid == userID {
						found = true
						break
					}
				}
				if !found {
					continue
				}

				payload, _ := json.Marshal(map[string]any{
					"id":         m.Values["id"],
					"thread_id":  threadID,
					"sender_id":  senderID,
					"body":       body,
					"nonce":      nonce,
					"created_at": createdAt,
				})
				fmt.Fprintf(writer, "data: %s\n\n", payload)
				flusher.Flush()

				// Remember where we are so a reconnect can resume after this.
				streamStart = m.ID
			}
		}
	}
}
