package controller

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

func CreateOrUpdateObject(ctx context.Context, client client.Client, object client.Object) error {
	logger := log.FromContext(ctx)
	if err := client.Create(ctx, object); err != nil {
		if errors.IsAlreadyExists(err) {
			if err = client.Update(ctx, object); err != nil {
				logger.Error(err, fmt.Sprintf("Could not update object %s/%s.", object.GetObjectKind().GroupVersionKind().Kind, object.GetName()))
				return err
			} else {
				logger.Info(fmt.Sprintf("Object %s/%s updated", object.GetObjectKind().GroupVersionKind().Kind, object.GetName()))
				return nil
			}
		} else {
			logger.Error(err, fmt.Sprintf("Could not create object %s/%s.", object.GetObjectKind().GroupVersionKind().Kind, object.GetName()))
			return err
		}
	}
	logger.Info(fmt.Sprintf("Object %s/%s created", object.GetObjectKind(), object.GetName()))
	return nil
}

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
