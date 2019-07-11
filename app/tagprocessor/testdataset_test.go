package tagprocessor

import (
	"errors"
	"fmt"
	"github.impcloud.net/RSP-Inventory-Suite/utilities/helper"
	"strings"
)

type testDataset struct {
	tagReads     []*TagRead
	tags         []*Tag
	readTimeOrig int64
}

func newTestDataset(tagCount int) testDataset {
	ds := testDataset{}
	ds.initialize(tagCount)
	return ds
}

// will generate tagread objects but NOT ingest them yet
func (ds *testDataset) initialize(tagCount int) {
	ds.tagReads = make([]*TagRead, tagCount)
	ds.tags = make([]*Tag, tagCount)
	ds.readTimeOrig = helper.UnixMilliNow()

	for i := 0; i < tagCount; i++ {
		ds.tagReads[i] = generateReadData(ds.readTimeOrig)
	}

	// resetEvents()
}

// update the tag pointers based on actual ingested data
func (ds *testDataset) updateTagRefs() {
	for i, tagRead := range ds.tagReads {
		ds.tags[i] = inventory[tagRead.Epc]
	}
}

func (ds *testDataset) setRssi(tagIndex int, rssi int) {
	ds.tagReads[tagIndex].Rssi = rssi
}

func (ds *testDataset) setRssiAll(rssi int) {
	for _, tagRead := range ds.tagReads {
		tagRead.Rssi = rssi
	}
}

func (ds *testDataset) setLastReadOnAll(timestamp int64) {
	for _, tagRead := range ds.tagReads {
		tagRead.LastReadOn = timestamp
	}
}

func (ds *testDataset) readTag(tagIndex int, sensor *RfidSensor, rssi int, times int) {
	ds.setRssi(tagIndex, rssi)

	for i := 0; i < times; i++ {
		processReadData(ds.tagReads[tagIndex], sensor)
	}
}

func (ds *testDataset) readAll(sensor *RfidSensor, rssi int, times int) {
	for tagIndex := range ds.tagReads {
		ds.readTag(tagIndex, sensor, rssi, times)
	}
}

func (ds *testDataset) size() int {
	return len(ds.tagReads)
}

func (ds *testDataset) verifyAll(expectedState TagState, expectedSensor *RfidSensor) error {
	ds.updateTagRefs()

	var errs []string
	for i := range ds.tags {
		if err := ds.verifyTag(i, expectedState, expectedSensor); err != nil {
			errs = append(errs, err.Error())
		}
	}

	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "\n"))
	}
	return nil
}

func (ds *testDataset) verifyTag(tagIndex int, expectedState TagState, expectedSensor *RfidSensor) error {
	tag := ds.tags[tagIndex]

	if tag == nil {
		read := ds.tagReads[tagIndex]
		return fmt.Errorf("Expected tag index %d to not be nil! read object: %v\ninventory: %v", tagIndex, read, inventory)
	} else if tag.state != expectedState {
		return fmt.Errorf("tag state %v does not match expected state %v for tag index %d\n%v", tag.state, expectedState, tagIndex, tag)
	} else if tag.DeviceLocation != expectedSensor.DeviceId {
		return fmt.Errorf("tag location %v does not match expected sensor %v for tag index %d\n%v", tag.DeviceLocation, expectedSensor.DeviceId, tagIndex, tag)
	}

	return nil
}
