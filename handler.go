package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/anthdm/hollywood/actor"
	"github.com/anthdm/hollywood/remote"
	"github.com/savsgio/atreugo/v11"
)

type Handler struct {
	actors      *actor.Engine
	paymentsPID *actor.PID
}

func NewHandler(engine *actor.Engine, actorPID *actor.PID) *Handler {

	return &Handler{
		actors:      engine,
		paymentsPID: actorPID,
	}
}

func (h *Handler) PostPayments(ctx *atreugo.RequestCtx) error {
	req := PaymentRequest{
		RequestedAt: time.Now().UTC(),
	}

	if err := json.Unmarshal(ctx.Request.Body(), &req); err != nil {
		return ctx.JSONResponse(map[string]string{
			"error": "Invalid request body",
		}, http.StatusBadRequest)
	}

	slog.Info("Received payment request", "request", req)

	h.actors.Send(h.paymentsPID, &remote.TestMessage{Data: []byte("teste hello world")})

	slog.Info("Payment request sent to actor", "request", req)

	ctx.Response.SetStatusCode(http.StatusAccepted) // No content response
	return nil
}

func (h *Handler) GetSummary(ctx *atreugo.RequestCtx) error {
	args := ctx.QueryArgs()

	fromBytes := args.Peek("from")
	toBytes := args.Peek("to")

	from, _ := time.Parse(time.RFC3339Nano, string(fromBytes))
	to, _ := time.Parse(time.RFC3339Nano, string(toBytes))

	req := SummaryRequest{
		From: from,
		To:   to,
	}
	resp, err := h.actors.Request(h.paymentsPID, &req, time.Second*2).Result()
	if err != nil {
		slog.Error("failed to get summary from actor", "err", err.Error())
		return ctx.JSONResponse(map[string]string{"error": err.Error()}, http.StatusInternalServerError)
	}
	typedRes, ok := resp.(SummaryResponse)
	if !ok {
		slog.Error("response is not of type SummaryResponse", "type", fmt.Sprintf("%T", resp))

		return ctx.JSONResponse(map[string]string{"error": "wrong type"}, http.StatusInternalServerError)
	}
	return ctx.JSONResponse(typedRes, http.StatusOK)

}

func (h *Handler) PostPurge(ctx *atreugo.RequestCtx) error {
	h.actors.Send(h.paymentsPID, &PurgeRequest{})

	ctx.SetStatusCode(http.StatusOK)
	return nil
}
