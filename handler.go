package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"time"

	decimal "github.com/alpacahq/alpacadecimal"
	"github.com/anthdm/hollywood/actor"
	"github.com/buger/jsonparser"
	"github.com/savsgio/atreugo/v11"
)

type Handler struct {
	actors      *actor.Engine
	paymentsPID *actor.PID
	ch          chan func() PaymentRequest
}

func NewHandler(engine *actor.Engine, actorPID *actor.PID) *Handler {

	fnPRChan := make(chan func() PaymentRequest, 2000)
	han := &Handler{
		actors:      engine,
		paymentsPID: actorPID,
		ch:          fnPRChan,
	}

	go func() {
		for fnPR := range fnPRChan {
			req := fnPR()
			han.actors.Send(han.paymentsPID, &req)
		}
	}()
	return han
}

func (h *Handler) PostPayments(ctx *atreugo.RequestCtx) error {

	bodyBytes := ctx.Request.Body()
	copiedBody := make([]byte, len(bodyBytes))
	copy(copiedBody, bodyBytes)

	// slog.Info("Received payment request", "request", req)

	h.ch <- func() PaymentRequest {
		cid, _ := jsonparser.GetString(copiedBody, "correlationId")
		amount, _ := jsonparser.GetFloat(copiedBody, "amount")

		return PaymentRequest{
			CID:    cid,
			Amount: decimal.NewFromFloat(amount),
			//RequestedAt: time.Now().UTC(),
		}
	}

	// cid, _ := jsonparser.GetString(bodyBytes, "correlationId")
	// amount, _ := jsonparser.GetFloat(bodyBytes, "amount")

	// h.ch <- PaymentRequest{
	// 	CID:    cid,
	// 	Amount: decimal.NewFromFloat(amount),
	// 	//RequestedAt: time.Now().UTC(),
	// }

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
