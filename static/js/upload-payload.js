/* Shared durable-queue wire contract: window and service worker. */
globalThis.UploadPayload = Object.freeze({
    build(item) {
        if (!item.id || !item.projectId || !item.blob) throw new Error('Missing durable upload data');
        const blob = item.blob instanceof Blob ? item.blob : new Blob([item.blob], {type: item.mimeType || 'image/jpeg'});
        if (!blob.size) throw new Error('Empty upload data');
        const form = new FormData();
        form.append('file', blob, item.fileName || 'photo.jpg');
        const fields = {client_upload_id:'id', project_id:'projectId', worker_id:'workerId', worker_name:'workerName', caption:'caption', tag:'tag', batch_id:'batchId', building_id:'buildingId', unit_id:'unitId', scope:'scope', lat:'lat', lng:'lng'};
        for (const [wire, key] of Object.entries(fields)) {
            if (item[key] !== undefined && item[key] !== null && item[key] !== '') form.append(wire, item[key]);
        }
        return form;
    },
    validateAck(item, result) {
        if (!result || !Number.isSafeInteger(result.id) || result.id <= 0 || result.client_upload_id !== item.id) {
            throw new Error('Upload acknowledgment missing or mismatched; retaining photo for retry');
        }
        return result;
    }
});
