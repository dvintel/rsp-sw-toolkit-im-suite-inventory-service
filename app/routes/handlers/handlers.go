/* Apache v2 license
*  Copyright (C) <2019> Intel Corporation
*
*  SPDX-License-Identifier: Apache-2.0
 */

package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"github.com/intel/rsp-sw-toolkit-im-suite-go-odata/parser"
	"github.com/intel/rsp-sw-toolkit-im-suite-inventory-service/app/alert"
	"github.com/intel/rsp-sw-toolkit-im-suite-inventory-service/app/epccontext"
	"github.com/intel/rsp-sw-toolkit-im-suite-inventory-service/app/facility"
	"github.com/intel/rsp-sw-toolkit-im-suite-inventory-service/app/handheldevent"
	"github.com/intel/rsp-sw-toolkit-im-suite-inventory-service/app/routes/schemas"
	"github.com/intel/rsp-sw-toolkit-im-suite-inventory-service/app/tag"
	"github.com/intel/rsp-sw-toolkit-im-suite-inventory-service/pkg/web"
	"github.com/intel/rsp-sw-toolkit-im-suite-utilities/go-metrics"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
	"net/http"
	"reflect"
	"strings"
	"time"
)

// Inventory represents the User API method handler set.
type Inventory struct {
	MasterDB *sql.DB
	MaxSize  int
	Url      string
}

// Index is used for Docker Healthcheck commands to indicate
// whether the http server is up and running to take requests
//nolint:unparam
func (inve *Inventory) Index(ctx context.Context, writer http.ResponseWriter, request *http.Request) error {
	web.Respond(ctx, writer, "Inventory Service", http.StatusOK)
	return nil
}

// GetTags retrieves all Tags from the database
// 200 OK, 400 Bad Request, 500 Internal Error
//nolint[: dupl[, gocyclo,...]]
func (inve *Inventory) GetTags(ctx context.Context, writer http.ResponseWriter, request *http.Request) error {

	// Metrics
	metrics.GetOrRegisterGauge("Inventory.GetTags.Attempt", nil).Update(1)

	startTime := time.Now()
	defer metrics.GetOrRegisterTimer("Inventory.GetTags.Latency", nil).Update(time.Since(startTime))
	mMarshalLatency := metrics.GetOrRegisterTimer("Inventory.GetTags.Marshal-Latency", nil)
	mUnmarshalLatency := metrics.GetOrRegisterTimer("Inventory.GetTags.Unmarshal-Latency", nil)
	mConfidenceLatency := metrics.GetOrRegisterTimer("Inventory.GetTags.Confidence-Latency", nil)

	mSuccess := metrics.GetOrRegisterGauge("Inventory.GetTags.Success", nil)
	mRetrieveErr := metrics.GetOrRegisterGauge("Inventory.GetTags.Retrieve-Error", nil)
	mMarshalErr := metrics.GetOrRegisterGauge("Inventory.GetTags.Marshal-Error", nil)
	mUnmarshalErr := metrics.GetOrRegisterGauge("Inventory.GetTags.Unmarshal-Error", nil)
	mConfidenceErr := metrics.GetOrRegisterGauge("Inventory.GetTags.Confidence-Error", nil)

	url := request.URL.Query()
	var isConfidence bool
	var isSelect bool
	filterURL := url[parser.Select]
	copyOfSelectQuery := ""
	if len(filterURL) > 0 {
		isSelect = true
		copyOfSelectQuery = filterURL[0]
	}

	if strings.Contains(strings.Join(filterURL, ""), "confidence") {
		selectQuery := url.Get(parser.Select)
		selectQuery += ",facility_id,last_read,location_history"
		url.Set(parser.Select, selectQuery)
		isConfidence = true
	}

	tags, count, err := tag.Retrieve(inve.MasterDB, url, inve.MaxSize)
	if err != nil {
		mRetrieveErr.Update(1)
		return errors.Wrap(err, "Error retrieving Tag")
	}

	/// Check if count is set, if so, return totalCount for $count
	if count != nil && tags == nil {
		web.Respond(ctx, writer, count, http.StatusOK)
		return nil
	}

	// Convert []interface{} to []bytes
	marshalTimer := time.Now()
	tagsBytes, err := json.Marshal(tags)
	mMarshalLatency.Update(time.Since(marshalTimer))
	if err != nil {
		mMarshalErr.Update(1)
		return errors.Wrap(err, "marshaling []interface{} to []bytes")
	}

	var tagSlice []tag.Tag
	unmarshalTimer := time.Now()
	if err := json.Unmarshal(tagsBytes, &tagSlice); err != nil {
		mUnmarshalErr.Update(1)
		return errors.Wrap(err, "unmarshaling []bytes to []Tags")
	}
	mUnmarshalLatency.Update(time.Since(unmarshalTimer))

	if len(tagSlice) > 0 {
		// Applying confidence to tags
		confidenceTimer := time.Now()
		// If $select not set, calculate confidence.
		// if confidence is set in the $select, calculate confidence
		if isConfidence || !isSelect {
			if err := ApplyConfidence(inve.MasterDB, tagSlice, inve.Url); err != nil {
				mConfidenceErr.Update(1)
				return err
			}
		}
		mConfidenceLatency.Update(time.Since(confidenceTimer))
	}

	if len(tagSlice) < 1 {
		tagSlice = []tag.Tag{} // Return empty array
	}

	var resultSlice interface{}
	if isSelect {
		resultSlice = getTagForSelectFields(copyOfSelectQuery, &tagSlice)
	} else {
		resultSlice = tagSlice
	}

	if count != nil && resultSlice != nil {
		mSuccess.Update(1)
		web.Respond(ctx, writer, tag.Response{Results: resultSlice, Count: count.Count}, http.StatusOK)
		return nil
	}

	//If count only is requested
	if count != nil {
		mSuccess.Update(1)
		web.Respond(ctx, writer, count, http.StatusOK)
		return nil
	}

	mSuccess.Update(1)
	web.Respond(ctx, writer, tag.Response{Results: resultSlice}, http.StatusOK)
	return nil
}

// PostCurrentInventory is used to send current inventory snapshot to the cloud connector
//nolint:lll
func (inve *Inventory) PostCurrentInventory(ctx context.Context, writer http.ResponseWriter, request *http.Request) error {
	return processPostRequest(ctx, schemas.PostCurrentInventorySchema, inve.MasterDB, request, writer, inve.Url)
}

// GetSearchByProductID returns a list of unique EPCs matching the productId provided. Body parameters shall be provided in request
// body in JSON format.
//nolint:lll
func (inve *Inventory) GetSearchByProductID(ctx context.Context, writer http.ResponseWriter, request *http.Request) error {
	return processGetRequest(ctx, schemas.SearchByProductIdSchema, inve.MasterDB, request, writer, inve.Url)
}

// UpdateQualifiedState is for uploading inventory events such as those from a handheld RFID reader
//nolint[: lll[, dupl, ...]]
func (inve *Inventory) UpdateQualifiedState(ctx context.Context, writer http.ResponseWriter, request *http.Request) error {
	metrics.GetOrRegisterGauge("Inventory.UpdateQualifiedState.Attempt", nil).Update(1)
	mProcessRequestErr := metrics.GetOrRegisterGauge("Inventory.UpdateQualifiedState.ProcessRequest-Error", nil)
	mValidateRequestErr := metrics.GetOrRegisterGauge("Inventory.UpdateQualifiedState.ValidateRequest-Error", nil)
	mSuccess := metrics.GetOrRegisterGauge("Inventory.UpdateQualifiedState.Success", nil)

	var mapping tag.RequestBody

	validationErrors, err := readAndValidateRequest(request, schemas.UpdateQualifiedStateSchema, &mapping)

	if err != nil {
		mProcessRequestErr.Update(1)
		return err
	}

	if validationErrors != nil {
		mValidateRequestErr.Update(1)
		web.Respond(ctx, writer, validationErrors, http.StatusBadRequest)
		return errors.New("could not validate request invalid schema")
	}
	objectMap := make(map[string]string)
	objectMap["qualified_state"] = mapping.QualifiedState

	mSuccess.Update(1)
	return processUpdateRequest(ctx, inve.MasterDB, writer, mapping.Epc, mapping.FacilityID, objectMap)
	return nil
}

// SetEpcContext updates the tag's epc context with the value in the request
// 200 OK, 400 Bad Request, 500 Internal
// nolint[: lll[, dupl, ...]]
func (inve *Inventory) SetEpcContext(ctx context.Context, writer http.ResponseWriter, request *http.Request) error {
	metrics.GetOrRegisterGauge("Inventory.SetEpcContext.Attempt", nil).Update(1)
	mProcessRequestErr := metrics.GetOrRegisterGauge("Inventory.SetEpcContext.ProcessRequest-Error", nil)
	mValidateRequestErr := metrics.GetOrRegisterGauge("Inventory.SetEpcContext.ValidateRequest-Error", nil)
	mSuccess := metrics.GetOrRegisterGauge("Inventory.SetEpcContext.Success", nil)

	var mapping epccontext.PutBody

	validationErrors, err := readAndValidateRequest(request, schemas.SetEpcContextSchema, &mapping)

	if err != nil {
		mProcessRequestErr.Update(1)
		return err
	}
	if validationErrors != nil {
		mValidateRequestErr.Update(1)
		web.Respond(ctx, writer, validationErrors, http.StatusBadRequest)
		return errors.New("could not validate request invalid schema")
	}

	objectMap := make(map[string]string)
	objectMap["epc_context"] = mapping.EpcContext

	mSuccess.Update(1)
	return processUpdateRequest(ctx, inve.MasterDB, writer, mapping.Epc, mapping.FacilityID, objectMap)
	return nil
}

// DeleteEpcContext removes the tag's epc context value
// 200 OK, 400 Bad Request, 500 Internal
func (inve *Inventory) DeleteEpcContext(ctx context.Context, writer http.ResponseWriter, request *http.Request) error {

	// Metrics
	metrics.GetOrRegisterGauge("Inventory.DeleteEpcContext.Attempt", nil).Update(1)

	startTime := time.Now()
	defer metrics.GetOrRegisterTimer("Inventory.DeleteEpcContext.Latency", nil).Update(time.Since(startTime))
	mSuccess := metrics.GetOrRegisterGauge("Inventory.DeleteEpcContext.Success", nil)

	mProcessUpdateLatency := metrics.GetOrRegisterTimer("Inventory.DeleteEpcContext.ProcessUpdate-Latency", nil)

	mValidationErr := metrics.GetOrRegisterGauge("Inventory.DeleteEpcContext.Validation-Error", nil)
	mProcessUpdateErr := metrics.GetOrRegisterGauge("Inventory.DeleteEpcContext.ProcessUpdate-Error", nil)

	var mapping epccontext.DeleteBody

	validationErrors, err := readAndValidateRequest(request, schemas.DeleteEpcContextSchema, &mapping)

	if err != nil {
		mValidationErr.Update(1)
		return err
	}
	if validationErrors != nil {
		mValidationErr.Update(1)
		web.Respond(ctx, writer, validationErrors, http.StatusBadRequest)
		return nil
	}

	objectMap := make(map[string]string)
	objectMap["epc_context"] = ""

	processUpdateTimer := time.Now()
	if err := processUpdateRequest(ctx, inve.MasterDB, writer, mapping.Epc, mapping.FacilityID, objectMap); err != nil {
		mProcessUpdateErr.Update(1)
		return err
	}
	mProcessUpdateLatency.Update(time.Since(processUpdateTimer))

	mSuccess.Update(1)
	return processUpdateRequest(ctx, inve.MasterDB, writer, mapping.Epc, mapping.FacilityID, objectMap)
	return nil
}

// DeleteAllTags removes the tag's epc context value
// 204 StatusNoContent, 400 Bad Request, 500 Internal
func (inve *Inventory) DeleteAllTags(ctx context.Context, writer http.ResponseWriter, request *http.Request) error {
	log.Debugf("DeleteAllTags request received- content length = %d", request.ContentLength)
	var err error

	// Metrics
	metrics.GetOrRegisterGauge("Inventory.DeleteAllTags.Attempt", nil).Update(1)

	startTime := time.Now()
	defer metrics.GetOrRegisterTimer("Inventory.DeleteAllTags.Latency", nil).Update(time.Since(startTime))
	mSuccess := metrics.GetOrRegisterGauge("Inventory.DeleteAllTags.Success", nil)
	mDeleteErr := metrics.GetOrRegisterGauge("Inventory.DeleteAllTags.Delete-Error", nil)
	mDeleteLatency := metrics.GetOrRegisterTimer("Inventory.DeleteAllTags.Delete-Latency", nil)
	mSendDelCompleteErr := metrics.GetOrRegisterGauge("Inventory.DeleteAllTags.SendDelComplete-Error", nil)

	deleteAllTagsTimer := time.Now()
	if err = tag.DeleteTagCollection(inve.MasterDB); err != nil {
		mDeleteErr.Update(1)
		return errors.Wrap(err, "Error deleting tag collection")
	}
	mDeleteLatency.Update(time.Since(deleteAllTagsTimer))

	mSuccess.Update(1)
	web.Respond(ctx, writer, nil, http.StatusNoContent)

	log.Debugf("DeleteAllTags completes at %v", time.Now())

	go func() {
		completeMessage := new(alert.MessagePayload)
		if sendFail := completeMessage.SendDeleteTagCompletionAlertMessage(); sendFail != nil {
			mSendDelCompleteErr.Update(1)
			log.Warnf("Failed to send the delete completion alert message- %s", sendFail.Error())
		}
	}()

	return err
	return nil
}

// GetSearchByEpc returns a list of tags with their EPCs matching a pattern.
// Body parameters shall be provided in requestbody in JSON format.
func (inve *Inventory) GetSearchByEpc(ctx context.Context, writer http.ResponseWriter, request *http.Request) error {
	return processGetRequest(ctx, schemas.SearchByEpcSchema, inve.MasterDB, request, writer, inve.Url)
}

// GetFacilities retrieves all Facilities from the database
// 200 OK, 400 Bad Request, 500 Internal
//nolint:dupl
func (inve *Inventory) GetFacilities(ctx context.Context, writer http.ResponseWriter, request *http.Request) error {

	// Metrics
	metrics.GetOrRegisterGauge("Inventory.GetFacilities.Attempt", nil).Update(1)

	startTime := time.Now()
	defer metrics.GetOrRegisterTimer("Inventory.GetFacilities.Latency", nil).Update(time.Since(startTime))
	mRetrieveLatency := metrics.GetOrRegisterTimer("Inventory.GetFacilities.Retrieve-Latency", nil)

	mSuccess := metrics.GetOrRegisterGauge("Inventory.GetFacilities.Success", nil)
	mRetrieveErr := metrics.GetOrRegisterGauge("Inventory.GetFacilities.Retrieve-Error", nil)

	retrieveTimer := time.Now()
	facilities, count, err := facility.Retrieve(inve.MasterDB, request.URL.Query())
	if err != nil {
		mRetrieveErr.Update(1)
		return errors.Wrap(err, "error retrieving facility")
	}
	mRetrieveLatency.Update(time.Since(retrieveTimer))

	/// Check if count is set, if so, return totalCount for $count
	if count != nil && facilities == nil {
		mSuccess.Update(1)
		web.Respond(ctx, writer, count, http.StatusOK)
		return nil
	}

	resultSlice := reflect.ValueOf(facilities)

	if resultSlice.Len() < 1 {
		facilities = []interface{}{} // Return empty array
	}

	if count != nil && facilities != nil {
		mSuccess.Update(1)
		web.Respond(ctx, writer, facility.Response{Results: facilities, Count: count.Count}, http.StatusOK)
		return nil
	}
	// regardless of result, write it out to the response
	mSuccess.Update(1)
	web.Respond(ctx, writer, facility.Response{Results: facilities}, http.StatusOK)
	return nil
}

// GetHandheldEvents retrieves all Handheld events from the database
// 200 OK, 400 Bad Request, 500 Internal
//nolint:dupl
func (inve *Inventory) GetHandheldEvents(ctx context.Context, writer http.ResponseWriter, request *http.Request) error {

	metrics.GetOrRegisterGauge(`Inventory.GetHandheldEvents.Attempt`, nil).Update(1)

	startTime := time.Now()
	defer metrics.GetOrRegisterTimer("Inventory.GetHandheldEvents.Latency", nil).Update(time.Since(startTime))

	mRetrieveErr := metrics.GetOrRegisterGauge("Inventory.GetHandheldEvents.Retrieve-Error", nil)
	mSuccess := metrics.GetOrRegisterGauge(`Inventory.GetHandheldEvents.Success`, nil)
	mUpdateLatency := metrics.GetOrRegisterTimer("Inventory.GetHandheldEvents.Update-Latency", nil)

	updateTimer := time.Now()
	eventData, count, err := handheldevent.Retrieve(inve.MasterDB, request.URL.Query())
	if err != nil {
		mRetrieveErr.Update(1)
		return errors.Wrap(err, "error retrieving handheld events")
	}

	/// Check if count is set, if so, return totalCount for $count
	if count != nil && eventData == nil {
		web.Respond(ctx, writer, count, http.StatusOK)
		mSuccess.Update(1)
		return nil
	}

	resultSlice := reflect.ValueOf(eventData)

	if resultSlice.Len() < 1 {
		eventData = []interface{}{} // Return empty array
	}

	if count != nil && eventData != nil {
		web.Respond(ctx, writer, handheldevent.Response{Results: eventData, Count: count.Count}, http.StatusOK)
		mSuccess.Update(1)
		return nil
	}
	// regardless of result, write it out to the response
	web.Respond(ctx, writer, handheldevent.Response{Results: eventData}, http.StatusOK)

	mUpdateLatency.Update(time.Since(updateTimer))
	mSuccess.Update(1)
	return nil
}

// UpdateCoefficients updates the coefficients by facility_id (name) in the facility collection
// 200 successful, 404 NotFound, 500 internal error
// nolint[: dupl[, lll, ...]]
func (inve *Inventory) UpdateCoefficients(ctx context.Context, writer http.ResponseWriter, request *http.Request) error {

	// Metrics
	metrics.GetOrRegisterGauge("Inventory.UpdateCoefficients.Attempt", nil).Update(1)

	startTime := time.Now()
	defer metrics.GetOrRegisterTimer("Inventory.UpdateCoefficients.Latency", nil).Update(time.Since(startTime))

	mUpdateLatency := metrics.GetOrRegisterTimer("Inventory.UpdateCoefficients.Update-Latency", nil)

	mSuccess := metrics.GetOrRegisterGauge("Inventory.UpdateCoefficients.Success", nil)
	mUpdateErr := metrics.GetOrRegisterGauge("Inventory.UpdateCoefficients.Update-Error", nil)
	mValidationErr := metrics.GetOrRegisterGauge("Inventory.UpdateCoefficients.Validation-Error", nil)

	var requestBody facility.RequestBody

	validationErrors, err := readAndValidateRequest(request, schemas.CoefficientsSchema, &requestBody)

	if err != nil {
		mValidationErr.Update(1)
		return err
	}

	if validationErrors != nil {
		mValidationErr.Update(1)
		web.Respond(ctx, writer, validationErrors, http.StatusBadRequest)
		return nil
	}

	// build attributes to be updated
	var updateFacility facility.Facility
	updateFacility.Name = requestBody.FacilityID
	updateFacility.Coefficients.DailyInventoryPercentage = requestBody.DailyInventoryPercentage
	updateFacility.Coefficients.ProbUnreadToRead = requestBody.ProbUnreadToRead
	updateFacility.Coefficients.ProbInStoreRead = requestBody.ProbInStoreRead
	updateFacility.Coefficients.ProbExitError = requestBody.ProbExitError

	// Update by facility_id(name)
	updateTimer := time.Now()
	if err := facility.UpdateCoefficients(inve.MasterDB, updateFacility); err != nil {
		mUpdateErr.Update(1)
		return errors.Wrapf(err, "Update %s", requestBody.FacilityID)
	}
	mUpdateLatency.Update(time.Since(updateTimer))

	mSuccess.Update(1)
	web.Respond(ctx, writer, nil, http.StatusOK)
	return nil
}
