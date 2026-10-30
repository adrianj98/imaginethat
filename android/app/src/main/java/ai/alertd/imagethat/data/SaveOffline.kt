package ai.alertd.imagethat.data

import kotlinx.coroutines.CancellationException

/**
 * [saved], [failed] and [skipped] are keyed by destination path relative to the offline vault root,
 * e.g. "srv/vault/Trip/a.jpg". [skipped] files were already there with the same size (an earlier,
 * interrupted save got them), so they were not copied again.
 */
data class SaveReport(val saved: List<String>, val failed: Map<String, String>, val skipped: List<String> = emptyList())

/** A single safe path segment: no separators, never "." / ".." / blank. */
fun sanitizeSegment(s: String): String {
    val t = s.replace(Regex("[/\\\\\\u0000]"), "_").trim()
    return if (t.isEmpty() || t == "." || t == "..") "_" else t
}

/** Thrown when the session ends mid-save (Locked / WrongPassword); [partial] is what was done before. */
class SaveInterrupted(val reason: SourceError, val partial: SaveReport) : Exception(reason.message, reason)

fun offlinePrefix(serverLabel: String, vaultName: String) =
    "${sanitizeSegment(serverLabel)}/${sanitizeSegment(vaultName)}"

/** Where a save is: the file being copied and its bytes done of total (0 until known). */
data class SaveProgress(val name: String, val done: Long, val total: Long)

/**
 * Copies [items] (folders recursively) from [from] into [into] under [prefix]. A source the Go
 * engine decrypts ([VaultSource.copyOrigin]: local, folder, client-decrypt server, Drive) is copied
 * with the engine's fault-tolerant copy, one job per file: reads that fail part-way are retried and
 * resumed. A server-decrypt vault is streamed in [CHUNK]-byte pieces. Either way a file that fails
 * leaves no truncated ciphertext behind, and the other files carry on. [onProgress] follows the
 * file being copied.
 */
suspend fun saveOffline(
    from: VaultSource,
    items: List<Item>,
    into: LocalSource,
    prefix: String,
    onProgress: ((SaveProgress) -> Unit)? = null,
): SaveReport {
    val origin = from.copyOrigin
    val saved = ArrayList<String>()
    val failed = LinkedHashMap<String, String>()
    val skipped = ArrayList<String>()
    val root = "/" + prefix.split('/').filter { it.isNotEmpty() }.joinToString("/") { sanitizeSegment(it) }

    try {
        var dir = ""
        for (seg in root.split('/').filter { it.isNotEmpty() }) {
            dir = "$dir/$seg"
            mkdirTolerant(into, dir)
        }
    } catch (e: CancellationException) {
        throw e
    } catch (e: SourceError.Locked) {
        throw SaveInterrupted(e, SaveReport(saved, failed))
    } catch (e: SourceError.WrongPassword) {
        throw SaveInterrupted(e, SaveReport(saved, failed))
    } catch (e: Exception) {
        for (it in items) failed["${root.trimStart('/')}/${sanitizeSegment(it.name)}"] = e.message ?: e.toString()
        return SaveReport(saved, failed)
    }

    suspend fun copy(item: Item, destDir: String) {
        // item.path is only for reading from the (untrusted) source; the destination segment is sanitized.
        val seg = sanitizeSegment(item.name)
        val dest = childPath(destDir, seg)
        val key = dest.trimStart('/')
        try {
            if (item.isDir) {
                mkdirTolerant(into, dest)
                for (child in from.list(item.path)) copy(child, dest)
            } else if (key in saved || key in skipped) {
                // Two source names sanitized to the same segment in this run: report, never merge.
                failed[key] = "another item in this save has the same name"
            } else if (existingSize(into, dest) == item.size) {
                skipped += key
            } else {
                onProgress?.invoke(SaveProgress(seg, 0, item.size))
                if (origin != null) {
                    into.copyFrom(origin, item.path, dest, onProgress?.let { report -> { done: Long, total: Long -> report(SaveProgress(seg, done, total)) } })
                } else {
                    from.open(item.path).use { into.upload(destDir, seg, it, item.size) }
                }
                saved += key
            }
        } catch (e: CancellationException) {
            throw e
        } catch (e: SourceError.Locked) {
            throw e
        } catch (e: SourceError.WrongPassword) {
            throw e
        } catch (e: Exception) {
            failed[key] = e.message ?: e.toString()
        }
    }

    try {
        for (item in items) copy(item, root)
    } catch (e: SourceError.Locked) {
        throw SaveInterrupted(e, SaveReport(saved, failed, skipped))
    } catch (e: SourceError.WrongPassword) {
        throw SaveInterrupted(e, SaveReport(saved, failed, skipped))
    }
    return SaveReport(saved, failed, skipped)
}

/** Plaintext size of [path] in the offline vault, or null when nothing is there. */
private suspend fun existingSize(into: LocalSource, path: String): Long? = try {
    into.size(path)
} catch (_: SourceError.NotFound) {
    null
}

private suspend fun mkdirTolerant(into: LocalSource, path: String) {
    try {
        into.mkdir(path)
    } catch (_: SourceError.Exists) {
    }
}
