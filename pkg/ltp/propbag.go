package ltp

import (
	"encoding/binary"
	"fmt"
	"math"
	"time"
	"unicode/utf16"

	"github.com/grokify/outlook-pst-go/pkg/disk"
	"github.com/grokify/outlook-pst-go/pkg/ndb"
	"github.com/grokify/outlook-pst-go/pkg/util"
)

// PropertyEntry represents an entry in the property context BTH.
// See [MS-PST] Section 2.3.3.3 - PC BTH Record.
type PropertyEntry struct {
	PropID   PropID          // wPropId - Property identifier
	PropType PropType        // wPropType - Property type
	Value    util.HeapNodeID // dwValueHnid - For fixed: inline value; for variable: HID or NID
}

// PropertyBag provides access to properties in a Property Context (PC).
// See [MS-PST] Section 2.3.3 - Property Context (PC).
// PC is a specialized BTH where keys are property IDs and values contain
// property types and data (either inline or as HNID references).
type PropertyBag struct {
	heap *HeapOnNode
	bth  *BTH
	node *ndb.Node
}

// NewPropertyBag creates a PropertyBag from a node.
func NewPropertyBag(node *ndb.Node) (*PropertyBag, error) {
	heap, err := NewHeapOnNode(node)
	if err != nil {
		return nil, fmt.Errorf("failed to create heap: %w", err)
	}

	// Verify this is a Property Context
	clientSig := heap.ClientSignature()
	if clientSig != disk.HeapSigPC {
		return nil, fmt.Errorf("invalid PC signature: got 0x%02X, expected 0x%02X", clientSig, disk.HeapSigPC)
	}

	// Create BTH from heap's root
	rootID := heap.RootID()
	bth, err := NewBTH(heap, rootID)
	if err != nil {
		return nil, fmt.Errorf("failed to create BTH: %w", err)
	}

	return &PropertyBag{
		heap: heap,
		bth:  bth,
		node: node,
	}, nil
}

// Exists returns true if the property exists.
func (pb *PropertyBag) Exists(id PropID) bool {
	_, err := pb.bth.LookupUint16(uint16(id))
	return err == nil
}

// GetPropertyEntry gets the raw property entry for a property ID.
func (pb *PropertyBag) GetPropertyEntry(id PropID) (*PropertyEntry, error) {
	data, err := pb.bth.LookupUint16(uint16(id))
	if err != nil {
		return nil, fmt.Errorf("property not found: 0x%04X", id)
	}

	// Property entry is 6 bytes: type(2) + hnid(4)
	if len(data) < 6 {
		return nil, fmt.Errorf("property entry too small: %d bytes", len(data))
	}

	return &PropertyEntry{
		PropID:   id,
		PropType: PropType(binary.LittleEndian.Uint16(data[0:2])),
		Value:    util.HeapNodeID(binary.LittleEndian.Uint32(data[2:6])),
	}, nil
}

// Properties returns a list of all property IDs.
func (pb *PropertyBag) Properties() []PropID {
	var props []PropID
	for entry, err := range pb.bth.Entries() {
		if err != nil {
			continue
		}
		if len(entry.Key) >= 2 {
			propID := PropID(binary.LittleEndian.Uint16(entry.Key))
			props = append(props, propID)
		}
	}
	return props
}

// GetType returns the type of a property.
func (pb *PropertyBag) GetType(id PropID) (PropType, error) {
	entry, err := pb.GetPropertyEntry(id)
	if err != nil {
		return 0, err
	}
	return entry.PropType, nil
}

// GetRaw reads the raw bytes for a property.
func (pb *PropertyBag) GetRaw(id PropID) ([]byte, error) {
	entry, err := pb.GetPropertyEntry(id)
	if err != nil {
		return nil, err
	}

	return pb.readPropertyValue(entry)
}

// readPropertyValue reads the value for a property entry.
func (pb *PropertyBag) readPropertyValue(entry *PropertyEntry) ([]byte, error) {
	propType := entry.PropType
	hnid := entry.Value

	// MS-PST 2.3.3.4: values of 4 bytes or less are stored inline in
	// dwValueHnid. FILETIME/GUID/i8 are HNIDs like variable properties.
	fixedSize := propType.FixedSize()
	if fixedSize > 0 && fixedSize <= 4 {
		buf := make([]byte, 4)
		binary.LittleEndian.PutUint32(buf, uint32(hnid))
		return buf[:fixedSize], nil
	}

	if hnid.IsHeapID() {
		// Read from heap
		return pb.heap.Read(hnid.ToHeapID())
	}

	// It's a subnode reference - read from subnode
	nid := hnid.ToNodeID()
	subnode, err := pb.node.LookupSubnode(nid)
	if err != nil {
		return nil, fmt.Errorf("failed to find subnode 0x%X: %w", nid, err)
	}

	return subnode.ReadAll()
}

// GetInt16 reads an int16 property.
func (pb *PropertyBag) GetInt16(id PropID) (int16, error) {
	data, err := pb.GetRaw(id)
	if err != nil {
		return 0, err
	}
	if len(data) < 2 {
		return 0, fmt.Errorf("property 0x%04X: insufficient data for int16", id)
	}
	return int16(binary.LittleEndian.Uint16(data)), nil //nolint:gosec // G115: binary format reinterpretation, same bit width
}

// GetInt32 reads an int32 property.
func (pb *PropertyBag) GetInt32(id PropID) (int32, error) {
	data, err := pb.GetRaw(id)
	if err != nil {
		return 0, err
	}
	if len(data) < 4 {
		return 0, fmt.Errorf("property 0x%04X: insufficient data for int32", id)
	}
	return int32(binary.LittleEndian.Uint32(data)), nil //nolint:gosec // G115: binary format reinterpretation, same bit width
}

// GetInt64 reads an int64 property.
func (pb *PropertyBag) GetInt64(id PropID) (int64, error) {
	data, err := pb.GetRaw(id)
	if err != nil {
		return 0, err
	}
	if len(data) < 8 {
		return 0, fmt.Errorf("property 0x%04X: insufficient data for int64", id)
	}
	return int64(binary.LittleEndian.Uint64(data)), nil //nolint:gosec // G115: binary format reinterpretation
}

// GetBool reads a boolean property.
func (pb *PropertyBag) GetBool(id PropID) (bool, error) {
	data, err := pb.GetRaw(id)
	if err != nil {
		return false, err
	}
	if len(data) < 2 {
		return false, fmt.Errorf("property 0x%04X: insufficient data for bool", id)
	}
	return binary.LittleEndian.Uint16(data) != 0, nil
}

// GetString reads a string property.
func (pb *PropertyBag) GetString(id PropID) (string, error) {
	entry, err := pb.GetPropertyEntry(id)
	if err != nil {
		return "", err
	}

	data, err := pb.readPropertyValue(entry)
	if err != nil {
		return "", err
	}

	// Determine string encoding from property type
	switch entry.PropType {
	case PropTypeString:
		// Unicode (UTF-16LE)
		return decodeUTF16LE(data), nil
	case PropTypeString8:
		// ANSI - just treat as bytes for now
		// Note: proper ANSI handling would require codepage info
		return string(data), nil
	default:
		// Try to detect
		if len(data)%2 == 0 && hasNullTerminator16(data) {
			return decodeUTF16LE(data), nil
		}
		return string(data), nil
	}
}

// GetBinary reads a binary property.
func (pb *PropertyBag) GetBinary(id PropID) ([]byte, error) {
	return pb.GetRaw(id)
}

// GetTime reads a FILETIME property as time.
func (pb *PropertyBag) GetTime(id PropID) (uint64, error) {
	data, err := pb.GetRaw(id)
	if err != nil {
		return 0, err
	}
	if len(data) < 8 {
		return 0, fmt.Errorf("property 0x%04X: insufficient data for FILETIME", id)
	}
	return binary.LittleEndian.Uint64(data), nil
}

// decodeUTF16LE decodes a UTF-16LE byte slice to a Go string.
func decodeUTF16LE(data []byte) string {
	// Convert to uint16 slice
	if len(data)%2 != 0 {
		data = data[:len(data)-1]
	}

	u16s := make([]uint16, len(data)/2)
	for i := range u16s {
		u16s[i] = binary.LittleEndian.Uint16(data[i*2 : i*2+2])
	}

	// Remove null terminator if present
	if len(u16s) > 0 && u16s[len(u16s)-1] == 0 {
		u16s = u16s[:len(u16s)-1]
	}

	return string(utf16.Decode(u16s))
}

// hasNullTerminator16 checks if UTF-16 data has a null terminator.
func hasNullTerminator16(data []byte) bool {
	if len(data) < 2 {
		return false
	}
	// Check last two bytes for null
	return data[len(data)-2] == 0 && data[len(data)-1] == 0
}

// GetFloat32 reads a 32-bit floating point property.
func (pb *PropertyBag) GetFloat32(id PropID) (float32, error) {
	data, err := pb.GetRaw(id)
	if err != nil {
		return 0, err
	}
	if len(data) < 4 {
		return 0, fmt.Errorf("property 0x%04X: insufficient data for float32", id)
	}
	bits := binary.LittleEndian.Uint32(data)
	return math.Float32frombits(bits), nil
}

// GetFloat64 reads a 64-bit floating point property.
func (pb *PropertyBag) GetFloat64(id PropID) (float64, error) {
	data, err := pb.GetRaw(id)
	if err != nil {
		return 0, err
	}
	if len(data) < 8 {
		return 0, fmt.Errorf("property 0x%04X: insufficient data for float64", id)
	}
	bits := binary.LittleEndian.Uint64(data)
	return math.Float64frombits(bits), nil
}

// GetCurrency reads a currency property (64-bit fixed point, 4 decimal places).
func (pb *PropertyBag) GetCurrency(id PropID) (int64, error) {
	data, err := pb.GetRaw(id)
	if err != nil {
		return 0, err
	}
	if len(data) < 8 {
		return 0, fmt.Errorf("property 0x%04X: insufficient data for currency", id)
	}
	return int64(binary.LittleEndian.Uint64(data)), nil //nolint:gosec // G115: binary format reinterpretation
}

// GetGUID reads a GUID property.
func (pb *PropertyBag) GetGUID(id PropID) (util.GUID, error) {
	data, err := pb.GetRaw(id)
	if err != nil {
		return util.GUID{}, err
	}
	if len(data) < 16 {
		return util.GUID{}, fmt.Errorf("property 0x%04X: insufficient data for GUID", id)
	}
	return util.GUIDFromBytes(data), nil
}

// GetTimeValue reads a FILETIME property and converts to time.Time.
func (pb *PropertyBag) GetTimeValue(id PropID) (time.Time, error) {
	ft, err := pb.GetTime(id)
	if err != nil {
		return time.Time{}, err
	}
	return FileTimeToTime(ft), nil
}

// GetStringSlice reads a multi-value string property.
func (pb *PropertyBag) GetStringSlice(id PropID) ([]string, error) {
	entry, err := pb.GetPropertyEntry(id)
	if err != nil {
		return nil, err
	}

	if !entry.PropType.IsMultiValued() {
		return nil, fmt.Errorf("property 0x%04X: not a multi-value type", id)
	}

	data, err := pb.readPropertyValue(entry)
	if err != nil {
		return nil, err
	}

	return pb.parseMultiValueStrings(data, entry.PropType)
}

// parseMultiValueStrings parses multi-value string data.
func (pb *PropertyBag) parseMultiValueStrings(data []byte, propType PropType) ([]string, error) {
	if len(data) < 4 {
		return nil, fmt.Errorf("multi-value string: insufficient data for count")
	}

	// First 4 bytes is count
	count := int(binary.LittleEndian.Uint32(data[:4]))
	if count == 0 {
		return []string{}, nil
	}

	// Following 4*count bytes are offsets
	headerSize := 4 + 4*count
	if len(data) < headerSize {
		return nil, fmt.Errorf("multi-value string: insufficient data for offsets")
	}

	offsets := make([]uint32, count)
	for i := 0; i < count; i++ {
		offsets[i] = binary.LittleEndian.Uint32(data[4+i*4:])
	}

	// Parse strings
	results := make([]string, count)
	isUnicode := propType.BaseType() == PropTypeString

	for i := 0; i < count; i++ {
		start := int(offsets[i])
		var end int
		if i < count-1 {
			end = int(offsets[i+1])
		} else {
			end = len(data)
		}

		if start >= len(data) || end > len(data) || start > end {
			continue
		}

		strData := data[start:end]
		if isUnicode {
			results[i] = decodeUTF16LE(strData)
		} else {
			// Remove trailing null
			if len(strData) > 0 && strData[len(strData)-1] == 0 {
				strData = strData[:len(strData)-1]
			}
			results[i] = string(strData)
		}
	}

	return results, nil
}

// GetInt32Slice reads a multi-value int32 property.
//
//nolint:dupl // Type-specific multi-value reader pattern, not duplicate code
func (pb *PropertyBag) GetInt32Slice(id PropID) ([]int32, error) {
	entry, err := pb.GetPropertyEntry(id)
	if err != nil {
		return nil, err
	}

	if !entry.PropType.IsMultiValued() {
		return nil, fmt.Errorf("property 0x%04X: not a multi-value type", id)
	}

	data, err := pb.readPropertyValue(entry)
	if err != nil {
		return nil, err
	}

	// First 4 bytes is count
	if len(data) < 4 {
		return nil, fmt.Errorf("multi-value int32: insufficient data for count")
	}

	count := int(binary.LittleEndian.Uint32(data[:4]))
	if len(data) < 4+count*4 {
		return nil, fmt.Errorf("multi-value int32: insufficient data for values")
	}

	results := make([]int32, count)
	for i := 0; i < count; i++ {
		results[i] = int32(binary.LittleEndian.Uint32(data[4+i*4:])) //nolint:gosec // G115: binary format reinterpretation
	}

	return results, nil
}

// GetInt64Slice reads a multi-value int64 property.
//
//nolint:dupl // Type-specific multi-value reader pattern, not duplicate code
func (pb *PropertyBag) GetInt64Slice(id PropID) ([]int64, error) {
	entry, err := pb.GetPropertyEntry(id)
	if err != nil {
		return nil, err
	}

	if !entry.PropType.IsMultiValued() {
		return nil, fmt.Errorf("property 0x%04X: not a multi-value type", id)
	}

	data, err := pb.readPropertyValue(entry)
	if err != nil {
		return nil, err
	}

	// First 4 bytes is count
	if len(data) < 4 {
		return nil, fmt.Errorf("multi-value int64: insufficient data for count")
	}

	count := int(binary.LittleEndian.Uint32(data[:4]))
	if len(data) < 4+count*8 {
		return nil, fmt.Errorf("multi-value int64: insufficient data for values")
	}

	results := make([]int64, count)
	for i := 0; i < count; i++ {
		results[i] = int64(binary.LittleEndian.Uint64(data[4+i*8:])) //nolint:gosec // G115: binary format reinterpretation
	}

	return results, nil
}

// GetBinarySlice reads a multi-value binary property.
func (pb *PropertyBag) GetBinarySlice(id PropID) ([][]byte, error) {
	entry, err := pb.GetPropertyEntry(id)
	if err != nil {
		return nil, err
	}

	if !entry.PropType.IsMultiValued() {
		return nil, fmt.Errorf("property 0x%04X: not a multi-value type", id)
	}

	data, err := pb.readPropertyValue(entry)
	if err != nil {
		return nil, err
	}

	// First 4 bytes is count
	if len(data) < 4 {
		return nil, fmt.Errorf("multi-value binary: insufficient data for count")
	}

	count := int(binary.LittleEndian.Uint32(data[:4]))
	if count == 0 {
		return [][]byte{}, nil
	}

	// Following 4*count bytes are offsets
	headerSize := 4 + 4*count
	if len(data) < headerSize {
		return nil, fmt.Errorf("multi-value binary: insufficient data for offsets")
	}

	offsets := make([]uint32, count)
	for i := 0; i < count; i++ {
		offsets[i] = binary.LittleEndian.Uint32(data[4+i*4:])
	}

	// Parse binary values
	results := make([][]byte, count)
	for i := 0; i < count; i++ {
		start := int(offsets[i])
		var end int
		if i < count-1 {
			end = int(offsets[i+1])
		} else {
			end = len(data)
		}

		if start >= len(data) || end > len(data) || start > end {
			results[i] = []byte{}
			continue
		}

		results[i] = make([]byte, end-start)
		copy(results[i], data[start:end])
	}

	return results, nil
}

// GetTimeSlice reads a multi-value FILETIME property.
func (pb *PropertyBag) GetTimeSlice(id PropID) ([]time.Time, error) {
	entry, err := pb.GetPropertyEntry(id)
	if err != nil {
		return nil, err
	}

	if !entry.PropType.IsMultiValued() {
		return nil, fmt.Errorf("property 0x%04X: not a multi-value type", id)
	}

	data, err := pb.readPropertyValue(entry)
	if err != nil {
		return nil, err
	}

	// First 4 bytes is count
	if len(data) < 4 {
		return nil, fmt.Errorf("multi-value time: insufficient data for count")
	}

	count := int(binary.LittleEndian.Uint32(data[:4]))
	if len(data) < 4+count*8 {
		return nil, fmt.Errorf("multi-value time: insufficient data for values")
	}

	results := make([]time.Time, count)
	for i := 0; i < count; i++ {
		ft := binary.LittleEndian.Uint64(data[4+i*8:])
		results[i] = FileTimeToTime(ft)
	}

	return results, nil
}

// GetGUIDSlice reads a multi-value GUID property.
func (pb *PropertyBag) GetGUIDSlice(id PropID) ([]util.GUID, error) {
	entry, err := pb.GetPropertyEntry(id)
	if err != nil {
		return nil, err
	}

	if !entry.PropType.IsMultiValued() {
		return nil, fmt.Errorf("property 0x%04X: not a multi-value type", id)
	}

	data, err := pb.readPropertyValue(entry)
	if err != nil {
		return nil, err
	}

	// First 4 bytes is count
	if len(data) < 4 {
		return nil, fmt.Errorf("multi-value GUID: insufficient data for count")
	}

	count := int(binary.LittleEndian.Uint32(data[:4]))
	if len(data) < 4+count*16 {
		return nil, fmt.Errorf("multi-value GUID: insufficient data for values")
	}

	results := make([]util.GUID, count)
	for i := 0; i < count; i++ {
		results[i] = util.GUIDFromBytes(data[4+i*16:])
	}

	return results, nil
}
