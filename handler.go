package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/anthdm/hollywood/actor"
	"github.com/buger/jsonparser"
	"github.com/savsgio/atreugo/v11"
)

type Handler struct {
	actors      *actor.Engine
	paymentsPID *actor.PID
	ch          chan PaymentRequest
}

func NewHandler(engine *actor.Engine, actorPID *actor.PID) *Handler {

	ch := make(chan PaymentRequest, 1000)
	han := &Handler{
		actors:      engine,
		paymentsPID: actorPID,
		ch:          ch,
	}

	go func() {
		for r := range ch {
			han.actors.Send(han.paymentsPID, &r)
		}
	}()
	return han
}

func (h *Handler) PostPayments(ctx *atreugo.RequestCtx) error {
	req := PaymentRequest{}

	bodyBytes := ctx.Request.Body()
	cid, _ := jsonparser.GetString(bodyBytes, "correlationId")
	amount, _ := jsonparser.GetFloat(bodyBytes, "amount")

	req.CID = cid
	req.Amount = amount

	// slog.Info("Received payment request", "request", req)

	h.ch <- req

	// slog.Info("Payment request sent to actor", "request", req)

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
	resp, err := h.actors.Request(h.paymentsPID, &req, 1500*time.Millisecond).Result()
	if err != nil {
		slog.Error("failed to get summary from actor", "err", err.Error())
		return ctx.JSONResponse(map[string]string{"error": err.Error()}, http.StatusInternalServerError)
	}
	typedRes, ok := resp.(SummaryResponse)
	if !ok {
		slog.Error("response is not of type SummaryResponse", "type", fmt.Sprintf("%T", resp))
		return ctx.JSONResponse(map[string]string{"error": "wrong type"}, http.StatusInternalServerError)
	}

	slog.Info("Get Summary ready", "summary", typedRes)
	return ctx.JSONResponse(typedRes, http.StatusOK)

}

func (h *Handler) PostPurge(ctx *atreugo.RequestCtx) error {
	h.actors.Send(h.paymentsPID, &PurgeRequest{})
	slog.Warn("Requesting purge of dataset...")
	ctx.SetStatusCode(http.StatusOK)
	return nil
}
