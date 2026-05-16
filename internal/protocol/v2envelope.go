package protocol

// Mobile Protocol v2 inner-frame wire types (docs/protocol-mobile.md
// § Wire shapes). The outer routing envelope is unchanged from v1; v2
// only changes the inner Frame to a discriminated {v, type, data} shape.
// Data is base64.StdEncoding (padded); decoded length cap is 65535 bytes
// (Noise framework per-message limit).

// V2Version is the major version number carried in InnerFrameV2.Version.
// Receivers MUST reject mismatched values with WS close 4421 (protocol
// mismatch).
const V2Version = 2

// Inner-frame discriminator values for InnerFrameV2.Type
// (docs/protocol-mobile.md § Wire shapes table).
const (
	TypeNoiseInit = "noise_init"
	TypeNoiseResp = "noise_resp"
	TypeNoiseMsg  = "noise_msg"
)

// InnerFrameV2 is the v2 inner-frame shape carried inside
// RoutingEnvelope.Frame (docs/protocol-mobile.md § Wire shapes).
type InnerFrameV2 struct {
	Version int    `json:"v"`
	Type    string `json:"type"`
	Data    string `json:"data"`
}
