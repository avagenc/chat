package api

import (
	"encoding/json"
	"log"
	"net/http"
)

type Response struct {
	Success bool   `json:"success"`
	Code    string `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
	Meta    any    `json:"meta,omitempty"`
	Errors  any    `json:"errors,omitempty"`
}

func Respond(w http.ResponseWriter, status int, resp Response) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		log.Printf("failed to encode response: %v", err)
	}
}

func NewSuccessResponse(message string, data any, meta any) Response {
	return Response{
		Success: true,
		Code:    "SUCCESS",
		Message: message,
		Data:    data,
		Meta:    meta,
	}
}

func NewErrorResponse(code string, message string, errs any) Response {
	return Response{
		Success: false,
		Code:    code,
		Message: message,
		Errors:  errs,
	}
}
