# Only closed vocabulary, booleans and counts may leave the fixture. Names,
# messages, UIDs, spec data, lease identities and timestamps stay in memory.
def known($allowed): . as $v | if ($allowed | index($v)) != null then $v else "other" end;
def finalizers:
  [.metadata.finalizers[]? | select(. == "execution.mecatl.dev/retain-workspace" or . == "execution.mecatl.dev/verify-termination" or . == "kubernetes.io/pvc-protection")] | unique;
def quota_keys:
  ["pods", "persistentvolumeclaims", "count/executionenvironments.execution.mecatl.dev", "requests.cpu", "requests.memory", "requests.storage", "requests.ephemeral-storage", "limits.cpu", "limits.memory", "limits.ephemeral-storage"];
[.[] | .items[]?] as $items |
($items | map(select(.kind == "Pod"))) as $pods |
($items | map(select(.kind == "PersistentVolumeClaim"))) as $pvcs |
($items | map(select(.kind == "ExecutionEnvironment"))) as $envs |
{kind:"collection", unavailable:([.[] | select(.unavailable == true)] | length), environments:($envs|length), pods:($pods|length), pvcs:($pvcs|length)},
($envs[:32][] | . as $env |
  ([$pods[] | select(.metadata.name == $env.status.pod.name)][0]) as $pod |
  ([$pvcs[] | select(.metadata.name == $env.status.pvc.name)][0]) as $pvc |
  {kind:"environment", deleting:(.metadata.deletionTimestamp != null), finalizers:finalizers,
   lifecycle:(.status.lifecycleOperation.type | known(["DeleteRetiredEnvironment","RetireEnvironment","ReplaceExecutor"])),
   phase:(.status.lifecycleOperation.phase | known(["DeletingPVC","ReleasingSlot","Quiescing","WaitingForTermination","RemovingPodFinalizer","WaitingForPodDeletion","CreatingReplacement"])),
   pod_present:($pod != null), pod_uid_matches:($pod != null and .status.pod.uid != null and .status.pod.uid == $pod.metadata.uid),
   pvc_present:($pvc != null), pvc_uid_matches:($pvc != null and .status.pvc.uid != null and .status.pvc.uid == $pvc.metadata.uid),
   operation_pvc_uid_matches:(.status.lifecycleOperation.expectedPVCUID != null and .status.lifecycleOperation.expectedPVCUID == .status.pvc.uid),
   operation_pod_uid_matches:(.status.lifecycleOperation.expectedPodUID != null and .status.lifecycleOperation.expectedPodUID == .status.pod.uid),
   lease_present:(.status.activeOperation.expiresAt != null),
   lease_expired:((.status.activeOperation.expiresAt // "" | sub("\\.[0-9]+Z$"; "Z") | try fromdateiso8601 catch null) as $expiry | if $expiry == null then null else $expiry <= now end),
   conditions:[.status.conditions[:16][]? | {type:(.type | known(["Ready","Retired","ExecutorTerminated","DeletionBlocked"])), status:(.status | known(["True","False","Unknown"])), reason:(.reason | known(["FenceUnknown","ExactLifecycleRequired","WorkspaceRetained","TerminalPodProof","AwaitingTerminalExecutor","Retained","ReplacementStarting","ReplacementReady","PVCUnavailable","ExecutorUnavailable","Reconciled","InvalidProfile","IncompatibleSchema"]))}]}),
($pods[:64][] | {kind:"pod", deleting:(.metadata.deletionTimestamp != null), finalizers:finalizers,
 phase:(.status.phase | known(["Pending","Running","Succeeded","Failed","Unknown"])),
 containers:([.status.containerStatuses[]?, .status.initContainerStatuses[]?, .status.ephemeralContainerStatuses[]?][:16] | map({terminated:(.state.terminated != null), reason:((.state.terminated.reason // .state.waiting.reason) | known(["Completed","Error","OOMKilled","ContainerStatusUnknown","CrashLoopBackOff","ImagePullBackOff","ErrImagePull","ContainerCreating"]))}))}),
($pvcs[:64][] | {kind:"pvc", deleting:(.metadata.deletionTimestamp != null), finalizers:finalizers}),
($items | map(select(.kind == "ResourceQuota")) | .[:32][] | . as $q |
 {kind:"quota", missing_or_mismatched_keys:[quota_keys[] | . as $key | select($q.spec.hard[$key] != null and ($q.status.hard[$key] != $q.spec.hard[$key] or $q.status.used[$key] == null))]}),
($items | map(select(.kind == "Event") | .reason | known(["FailedScheduling","FailedMount","FailedAttachVolume","FailedBinding","ProvisioningFailed","FailedCreate","Failed","BackOff","Killing","Scheduled","Pulled","Created","Started"])) | group_by(.)[] | {kind:"event", reason:.[0], count:length}),
(if any(.[]; .unavailable == true) then "" | halt_error(1) else empty end)
