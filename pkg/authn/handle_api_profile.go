// Copyright 2022 Paul Greenberg greenpau@outlook.com
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package authn

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/greenpau/go-authcrunch/pkg/requests"

	"regexp"

	"github.com/greenpau/go-authcrunch/pkg/user"
	addrutil "github.com/greenpau/go-authcrunch/pkg/util/addr"
	"go.uber.org/zap"
)

var tokenSecretRegexPattern = regexp.MustCompile(`^[A-Za-z0-9]{10,200}$`)
var tokenIssuerRegexPattern = regexp.MustCompile(`^[A-Za-z0-9]{3,50}$`)
var tokenDescriptionRegexPattern = regexp.MustCompile(`^[\w\s\-\_,\.]{3,255}$`)
var tokenPasscodeRegexPattern = regexp.MustCompile(`^[0-9]{4,8}$`)

func handleAPIProfileResponse(w http.ResponseWriter, rr *requests.Request, code int, resp map[string]interface{}) error {
	resp["status"] = code
	rr.Response.Code = code
	respBytes, _ := json.Marshal(resp)
	w.WriteHeader(rr.Response.Code)
	w.Write(respBytes)
	return nil
}

func (p *Portal) handleAPIProfile(ctx context.Context, w http.ResponseWriter, r *http.Request, rr *requests.Request, parsedUser *user.User) error {
	resp := make(map[string]interface{})
	resp["timestamp"] = time.Now().UTC().Format(time.RFC3339Nano)

	if parsedUser == nil {
		resp["message"] = "Profile API received nil user"
		return handleAPIProfileResponse(w, rr, http.StatusBadRequest, resp)
	}

	usr, err := p.sessions.Get(parsedUser.Claims.ID)
	if err != nil {
		p.logger.Warn(
			"jti session not found",
			zap.String("session_id", rr.Upstream.SessionID),
			zap.String("request_id", rr.ID),
			zap.String("jti", parsedUser.Claims.ID),
			zap.Any("error", err),
			zap.String("source_address", addrutil.GetSourceAddress(r)),
		)
		resp["message"] = "Profile API failed to locate JTI session"
		return handleAPIProfileResponse(w, rr, http.StatusUnauthorized, resp)
	}

	if permitted := usr.HasRole("authp/admin", "authp/user"); !permitted {
		resp["message"] = "Profile API did not find valid role for the user"
		return handleAPIProfileResponse(w, rr, http.StatusForbidden, resp)
	}

	// Unpack the request and determine the type of the request.
	var reqKind = "unknown"

	// Read the request body
	defer r.Body.Close()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		resp["message"] = "Profile API failed to parse request body"
		return handleAPIProfileResponse(w, rr, http.StatusBadRequest, resp)
	}
	var bodyData map[string]interface{}
	if err := json.Unmarshal(body, &bodyData); err != nil {
		resp["message"] = "Profile API failed to parse request JSON body"
		return handleAPIProfileResponse(w, rr, http.StatusBadRequest, resp)
	}

	if v, exists := bodyData["kind"]; exists {
		reqKind = v.(string)
	}

	switch reqKind {
	case "fetch_user_dashboard_data":
	case "delete_user_multi_factor_authenticator":
	case "fetch_user_multi_factor_authenticators":
	case "fetch_user_multi_factor_authenticator":
	case "fetch_user_app_multi_factor_authenticator_code":
	case "test_user_app_multi_factor_authenticator":
	case "add_user_app_multi_factor_authenticator":
	default:
		resp["message"] = "Profile API recieved unsupported request type"
		return handleAPIProfileResponse(w, rr, http.StatusBadRequest, resp)
	}

	// Determine supported authentication methods.

	switch usr.Authenticator.Method {
	case "local":
	default:
		resp["message"] = fmt.Sprintf("%s is not supported with profile API", usr.Authenticator.Method)
		return handleAPIProfileResponse(w, rr, http.StatusNotImplemented, resp)
	}

	backend := p.getIdentityStoreByRealm(usr.Authenticator.Realm)
	if backend == nil {
		p.logger.Warn(
			"backend not found",
			zap.String("session_id", rr.Upstream.SessionID),
			zap.String("request_id", rr.ID),
			zap.String("realm", usr.Authenticator.Realm),
			zap.String("jti", usr.Claims.ID),
			zap.String("source_address", addrutil.GetSourceAddress(r)),
		)
		resp["message"] = fmt.Sprintf("backend for %s realm not found", usr.Authenticator.Realm)
		return handleAPIProfileResponse(w, rr, http.StatusBadRequest, resp)
	}

	p.logger.Debug(
		"backend found for handling api request",
		zap.String("session_id", rr.Upstream.SessionID),
		zap.String("request_id", rr.ID),
		zap.String("realm", usr.Authenticator.Realm),
		zap.String("jti", usr.Claims.ID),
		zap.String("request_kind", reqKind),
		zap.String("source_address", addrutil.GetSourceAddress(r)),
	)

	// Populate username (sub) and email address (email)
	rr.User.Username = usr.Claims.Subject
	rr.User.Email = usr.Claims.Email

	switch reqKind {
	case "fetch_user_dashboard_data":
		return p.FetchUserDashboardData(ctx, w, r, rr, parsedUser, resp, usr, backend)
	case "fetch_user_multi_factor_authenticators":
		return p.FetchUserMultiFactorVerifiers(ctx, w, r, rr, parsedUser, resp, usr, backend)
	case "fetch_user_multi_factor_authenticator":
		return p.FetchUserMultiFactorVerifier(ctx, w, r, rr, parsedUser, resp, usr, backend, bodyData)
	case "delete_user_multi_factor_authenticator":
		return p.DeleteUserMultiFactorVerifier(ctx, w, r, rr, parsedUser, resp, usr, backend, bodyData)
	case "fetch_user_app_multi_factor_authenticator_code":
		return p.FetchUserAppMultiFactorVerifierCode(ctx, w, r, rr, parsedUser, resp, usr, backend, bodyData)
	case "test_user_app_multi_factor_authenticator":
		return p.TestUserAppMultiFactorVerifier(ctx, w, r, rr, parsedUser, resp, usr, backend, bodyData)
	case "add_user_app_multi_factor_authenticator":
		return p.AddUserAppMultiFactorVerifier(ctx, w, r, rr, parsedUser, resp, usr, backend, bodyData)
	}

	// Default response
	resp["message"] = fmt.Sprintf("unsupported %s request kind with profile API", reqKind)
	return handleAPIProfileResponse(w, rr, http.StatusNotImplemented, resp)
}
