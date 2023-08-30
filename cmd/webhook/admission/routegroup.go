package admission

import (
	"encoding/json"

	"github.com/zalando/skipper/dataclients/kubernetes/definitions"
)

type RouteGroupAdmitter struct {
	RouteGroupValidator *definitions.RouteGroupValidator
}

func (rga *RouteGroupAdmitter) name() string {
	return "routegroup"
}

func (rga *RouteGroupAdmitter) admit(req *admissionRequest) (*admissionResponse, error) {
	var rgItem definitions.RouteGroupItem
	if err := json.Unmarshal(req.Object, &rgItem); err != nil {
		return nil, err
	}

	if err := rga.RouteGroupValidator.Validate(&rgItem); err != nil {
		return &admissionResponse{
			UID:     req.UID,
			Allowed: false,
			Result:  &status{Message: err.Error()},
		}, nil
	}

	if rgItem.Metadata.Name == "admission-test" {
		return &admissionResponse{
			UID:     req.UID,
			Allowed: true,
			Warnings: []string{
				"This is a test warning1, see https://opensource.zalando.com/skipper/kubernetes/routegroups/",
				`Argument "foo" is not allowed for filter fooBarBazQux due to whatever, see https://opensource.zalando.com/skipper/kubernetes/routegroups/ for details.`,
			},
		}, nil
	}

	return &admissionResponse{
		UID:     req.UID,
		Allowed: true,
	}, nil
}
