package ai.alertd.imagethat.data

import kotlinx.coroutines.CancellationException

/** [saved] and [failed] are keyed by destination path relative to the offline vault root, e.g. "srv/vault/Trip/a.jpg". */
data class SaveReport(val saved: List<String>, val failed: Map<String, String>)

/** A single safe path segment: no separators, never "." / ".." / blank. */
fun sanitizeSegment(s: String): String {
    val t = s.replace(Regex("[/\\\\\\u0000]"), "_").trim()
    return if (t.isEmpty() || t == "." || t == "..") "_" else t
}

fun offlinePrefix(serverLabel: String, vaultName: String) =
    "${sanitizeSegment(serverLabel)}/${sanitizeSegment(vaultName)}"

/**
 * Copies [items] (folders recursively) from [from] into [into] under [prefix],
 * streaming each file in [CHUNK]-byte pieces. A file that fails part-way is
 * aborted by [LocalSource.upload] so no truncated ciphertext remains; other
 * files carry on.
 */
suspend fun saveOffline(from: VaultSource, items: List<Item>, into: LocalSource, prefix: String): SaveReport {
    val saved = ArrayList<String>()
    val failed = LinkedHashMap<String, String>()
    val root = "/" + prefix.trim('/')

    try {
        var dir = ""
        for (seg in root.split('/').filter { it.isNotEmpty() }) {
            dir = "$dir/$seg"
            mkdirTolerant(into, dir)
        }
    } catch (e: CancellationException) {
        throw e
    } catch (e: Exception) {
        for (it in items) failed["${root.trimStart('/')}/${it.name}"] = e.message ?: e.toString()
        return SaveReport(saved, failed)
    }

    suspend fun copy(item: Item, destDir: String) {
        val dest = childPath(destDir, item.name)
        val key = dest.trimStart('/')
        try {
            if (item.isDir) {
                mkdirTolerant(into, dest)
                for (child in from.list(item.path)) copy(child, dest)
            } else {
                from.open(item.path).use { into.upload(destDir, item.name, it, item.size) }
                saved += key
            }
        } catch (e: CancellationException) {
            throw e
        } catch (e: Exception) {
            failed[key] = e.message ?: e.toString()
        }
    }

    for (item in items) copy(item, root)
    return SaveReport(saved, failed)
}

private suspend fun mkdirTolerant(into: LocalSource, path: String) {
    try {
        into.mkdir(path)
    } catch (_: SourceError.Exists) {
    }
}
