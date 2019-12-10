/* Apache v2 license
*  Copyright (C) <2019> Intel Corporation
*
*  SPDX-License-Identifier: Apache-2.0
 */

package main

import (
	"fmt"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
	"github.impcloud.net/RSP-Inventory-Suite/inventory-service/app/cloudconnector"
	"github.impcloud.net/RSP-Inventory-Suite/inventory-service/app/config"
	"github.impcloud.net/RSP-Inventory-Suite/inventory-service/app/routes/handlers"
	"github.impcloud.net/RSP-Inventory-Suite/inventory-service/app/rules"
	"github.impcloud.net/RSP-Inventory-Suite/inventory-service/app/tag"
	"github.impcloud.net/RSP-Inventory-Suite/inventory-service/pkg/jsonrpc"
	"github.impcloud.net/RSP-Inventory-Suite/inventory-service/pkg/statemodel"
	"github.impcloud.net/RSP-Inventory-Suite/utilities/go-metrics"
	"github.impcloud.net/RSP-Inventory-Suite/utilities/helper"
	"time"
)

// SkuMapping struct for the sku-mapping service
type SkuMapping struct {
	url string
}

// NewSkuMapping initialize new SkuMapping
func NewSkuMapping(url string) SkuMapping {
	return SkuMapping{
		url: url,
	}
}

// processTagData inserts data from Edgex into database
//nolint :gocyclo
func (skuMapping SkuMapping) processTagData(invApp *inventoryApp, invEvent *jsonrpc.InventoryEvent, source string, tagsGauge *metrics.GaugeCollection) error {

	numberOfTags := len(invEvent.Params.Data)
	if numberOfTags == 0 {
		return nil
	}

	mProcessTagLatency := metrics.GetOrRegisterTimer(`Inventory.ProcessTagData-Latency`, nil)
	processTagTimer := time.Now()

	var tagData []tag.Tag
	var tagStateChangeList []tag.TagStateChange

	// todo: is below comment still valid?
	// POC only implementation
	currentTimeMillis := helper.UnixMilliNow()

	if tagsGauge != nil {
		(*tagsGauge).Add(int64(numberOfTags))
	}
	log.Debugf("Processing %d Tag Events", numberOfTags)
	tagsFiltered := 0

	for _, tempTag := range invEvent.Params.Data {
		if len(config.AppConfig.EpcFilters) > 0 {
			// ignore tags that don't match our filters
			if !statemodel.IsTagWhitelisted(tempTag.EpcCode, config.AppConfig.EpcFilters) {
				continue
			}
		}

		// todo: is below comment still valid?
		// POC only implementation
		markDepartedIfUnseen(&tempTag, config.AppConfig.AgeOuts, currentTimeMillis)

		// Add source & event
		if source == "handheld" {
			tempTag.EventType = statemodel.ArrivalEvent
		}

		// Note: If bottlenecks may need to redesign to eliminate large number
		// of queries to DB currently this will make a call to the DB PER tag
		tagFromDB, err := tag.FindByEpc(invApp.masterDB, tempTag.EpcCode)

		if err != nil {
			return errors.Wrap(err, "Error retrieving tag from database")
		}

		updatedTag := statemodel.UpdateTag(tagFromDB, tempTag, source)

		tagData = append(tagData, updatedTag)

		var tagStateChange tag.TagStateChange
		tagStateChange.PreviousState = tagFromDB
		tagStateChange.CurrentState = updatedTag

		if tagStateChange.PreviousState.IsEqual(tag.Tag{}) != true &&
			tagStateChange.CurrentState.IsEqual(tag.Tag{}) != true {
			tagStateChangeList = append(tagStateChangeList, tagStateChange)
		}

		log.Trace("Previous and Current Tag State:\n")
		log.Trace(tagStateChange)
	}

	log.Debugf("Filtered %d Tags.", tagsFiltered)

	// If at least 1 tag passed the whitelist, then insert
	if len(tagData) > 0 {

		if err := tag.Replace(invApp.masterDB, tagData); err != nil {
			return errors.Wrap(err, "error replacing tags")
		}

		if err := handlers.ApplyConfidence(invApp.masterDB, tagData, skuMapping.url); err != nil {
			return err
		}

		handlers.UpdateForCycleCount(tagData)

		if config.AppConfig.CloudConnectorUrl != "" {
			go func() {
				if err := cloudconnector.SendEvent(invEvent, tagData); err != nil {
					log.WithFields(log.Fields{
						"Method": "processTagData",
						"Action": "Trigger Cloud Connector",
						"Error":  err.Error(),
					}).Error(err)
				}
			}()
		}

		if config.AppConfig.RulesUrl != "" {
			go func() {
				if err := rules.ApplyRules(source, tagStateChangeList); err != nil {
					log.WithFields(log.Fields{
						"Method": "processTagData",
						"Action": "Apply Rules",
						"Error":  fmt.Sprintf("%+v", err),
					}).Error(err)
				}
			}()
		}

		go invApp.pushEventsToCoreData(currentTimeMillis, invEvent.Params.ControllerId, tagData)
	}

	mProcessTagLatency.Update(time.Since(processTagTimer))

	return nil
}
