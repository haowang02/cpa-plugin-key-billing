package plugin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"cpa-key-billing/internal/billing"
)

const maxReferencePriceResponseBytes = 128 << 20

func (a *App) downloadReferencePrices(ctx context.Context) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	response, err := callHostHTTP(a.hostCaller, hostHTTPRequest{Method: http.MethodGet, URL: billing.ModelsDevPricesURL})
	if err != nil {
		// Transport URLs can contain proxy credentials or query parameters.
		var requestError *url.Error
		for errors.As(err, &requestError) {
			err = requestError.Err
		}
		return nil, fmt.Errorf("Download reference prices: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Download reference prices: HTTP %d", response.StatusCode)
	}
	if len(response.Body) > maxReferencePriceResponseBytes {
		return nil, fmt.Errorf("Reference price response exceeds the size limit")
	}
	return response.Body, nil
}

// The browser converts the amounts it shows between currencies with this rate.
func (a *App) currency(_ ManagementRequest, access viewAccess) ManagementResponse {
	return viewJSON(access, http.StatusOK, map[string]billing.ExchangeRate{"cny_per_usd": a.store.Config().CNYPerUSD})
}

func (a *App) listPrices(req ManagementRequest, access viewAccess) ManagementResponse {
	models := req.Query["model"]
	includeCustom := !access.APIKey
	if raw := req.Query.Get("include_custom"); raw != "" {
		value, err := strconv.ParseBool(raw)
		if err != nil {
			return viewJSONError(access, http.StatusBadRequest, "invalid", "include_custom must be true or false")
		}
		includeCustom = includeCustom && value
	}
	prices, err := a.store.ModelPriceRows(models, includeCustom)
	if err != nil {
		return viewErrorResponse(access, err)
	}
	return viewJSON(access, http.StatusOK, prices)
}

func (a *App) putPrices(req ManagementRequest) ManagementResponse {
	var price billing.CustomPrice
	if errDecode := decodeStrict(req.Body, &price); errDecode != nil {
		return errorResponse(errDecode)
	}
	stored, errUpsert := a.store.UpsertPrice(price)
	if errUpsert != nil {
		return errorResponse(errUpsert)
	}
	return JSONResponse(http.StatusOK, map[string]any{"price": stored})
}

func (a *App) searchReferencePrices(req ManagementRequest) ManagementResponse {
	limit := 20
	if raw := strings.TrimSpace(req.Query.Get("limit")); raw != "" {
		parsed, errParse := strconv.Atoi(raw)
		if errParse != nil || parsed < 1 || parsed > 50 {
			return JSONError(http.StatusBadRequest, "invalid", "Limit must be an integer from 1 to 50")
		}
		limit = parsed
	}
	prices, err := a.store.SearchReferencePrices(req.Query.Get("q"), limit)
	if err != nil {
		return errorResponse(err)
	}
	return JSONResponse(http.StatusOK, map[string]any{"prices": prices})
}

func (a *App) refreshReferencePrices() ManagementResponse {
	result, errRefresh := a.store.RefreshReferencePrices()
	if errRefresh != nil {
		return errorResponse(errRefresh)
	}
	return JSONResponse(http.StatusOK, result)
}

func (a *App) deletePrice(req ManagementRequest) ManagementResponse {
	if err := a.store.DeletePrice(req.Query.Get("model_id")); err != nil {
		return errorResponse(err)
	}
	return JSONResponse(200, map[string]any{"deleted": req.Query.Get("model_id")})
}

func (a *App) referencePriceStatus() ManagementResponse {
	return JSONResponse(200, map[string]any{"metadata": a.store.ReferencePriceMetadata()})
}
