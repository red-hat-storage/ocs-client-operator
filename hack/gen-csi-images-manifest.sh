#!/bin/bash

cd config/manager
rm -f csi-images.yaml

echo "Generating CSI image manifest for OCP versions: ${CSI_OCP_VERSIONS}"

echo -n "---" > csi-images.yaml
for version in ${CSI_OCP_VERSIONS}; do
  echo "" >> csi-images.yaml

  ver="${version//./_}"
  echo -e "- version: $version\n  containerImages:" >> csi-images.yaml

  csi_var="CSI_IMG_PROVISIONER_${ver}"
  echo "    provisionerImageURL: \"${!csi_var:-${CSI_IMG_PROVISIONER}}\"" >> csi-images.yaml

  csi_var="CSI_IMG_ATTACHER_${ver}"
  echo "    attacherImageURL: \"${!csi_var:-${CSI_IMG_ATTACHER}}\"" >> csi-images.yaml

  csi_var="CSI_IMG_RESIZER_${ver}"
  echo "    resizerImageURL: \"${!csi_var:-${CSI_IMG_RESIZER}}\"" >> csi-images.yaml

  csi_var="CSI_IMG_SNAPSHOTTER_${ver}"
  echo "    snapshotterImageURL: \"${!csi_var:-${CSI_IMG_SNAPSHOTTER}}\"" >> csi-images.yaml

  csi_var="CSI_IMG_REGISTRAR_${ver}"
  echo "    driverRegistrarImageURL: \"${!csi_var:-${CSI_IMG_REGISTRAR}}\"" >> csi-images.yaml

  csi_var="CSI_IMG_CEPH_CSI_${ver}"
  echo "    cephCSIImageURL: \"${!csi_var:-${CSI_IMG_CEPH_CSI}}\"" >> csi-images.yaml

  csi_var="CSI_IMG_ADDONS_${ver}"
  echo "    csiaddonsImageURL: \"${!csi_var:-${CSI_IMG_ADDONS}}\"" >> csi-images.yaml
done
