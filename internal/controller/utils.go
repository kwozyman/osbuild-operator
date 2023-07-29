package controller

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

func DeleteAllObjectsWithLabel(ctx context.Context, client client.Client, kind string, apiVersion string, label string, imageName string) error {
	logger := log.FromContext(ctx)
	u := unstructured.UnstructuredList{}
	u.SetKind(kind)
	u.SetAPIVersion(apiVersion)
	if err := client.List(ctx, &u); err != nil {
		logger.Error(err, fmt.Sprintf("Could not list objects %s/%s", kind, apiVersion))
		return err
	}
	for _, item := range u.Items {
		if item.GetLabels()[label] == imageName {
			if err := client.Delete(ctx, &item); err != nil {
				logger.Error(err, fmt.Sprintf("Could not delete object %s/%s", kind, item.GetName()))
				return err
			}
			logger.Info(fmt.Sprintf("Deleted object %s/%s", kind, item.GetName()))
		}
	}
	return nil
}
