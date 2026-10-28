package ai.alertd.imagethat.local

import android.content.ContentResolver
import android.net.Uri
import android.provider.DocumentsContract
import android.provider.DocumentsContract.Document
import java.io.FileInputStream
import java.io.FileNotFoundException
import java.io.IOException
import java.io.OutputStream
import java.nio.ByteBuffer
import mobile.Storage
import mobile.StorageWriter
import org.json.JSONArray
import org.json.JSONObject

/**
 * [Storage] over a user-picked SAF tree. Vault-relative paths ("" is the root)
 * are resolved to document ids by walking children. Child listings are cached
 * per directory in memory for the life of this object only, and dropped for a
 * directory whenever this object changes it. Nothing is cached on disk.
 *
 * Error messages start with "not_found" / "exists" as the Go side expects.
 */
class SafStorage(private val resolver: ContentResolver, private val treeUri: Uri) : Storage {
    private class Child(val docId: String, val name: String, val isDir: Boolean, val size: Long?)

    private val rootId: String = DocumentsContract.getTreeDocumentId(treeUri)
    private val cache = HashMap<String, Map<String, Child>>()

    /** Set once a SecurityException shows the grant is gone; every later call fails as unavailable. */
    @Volatile private var revoked = false

    private fun unavailable(cause: Throwable? = null) =
        IOException("$UNAVAILABLE: access to the picked folder was revoked or is gone", cause)

    /**
     * SecurityException cannot be told apart once it crosses gomobile (only the message
     * survives), so it is rethrown with the stable [UNAVAILABLE] prefix.
     */
    private inline fun <T> guard(block: () -> T): T {
        if (revoked) throw unavailable()
        try {
            return block()
        } catch (e: SecurityException) {
            revoked = true
            throw unavailable(e)
        }
    }

    /**
     * Write-side calls: a denied write is not proof the tree is gone (read-only grant or
     * provider mode), so it is reported as [READ_ONLY] and never sets [revoked].
     */
    private inline fun <T> guardWrite(block: () -> T): T {
        if (revoked) throw unavailable()
        try {
            return block()
        } catch (e: SecurityException) {
            throw readOnly(e)
        }
    }

    private fun readOnly(cause: Throwable) = IOException("$READ_ONLY: write access to the picked folder was denied", cause)

    companion object {
        /** Message prefix for a denied write; LocalError.from maps it to ReadOnly. */
        const val READ_ONLY = "read_only"

        /** Message prefix for revoked access; LocalError.from matches it anywhere in the (Go-wrapped) message. */
        const val UNAVAILABLE = "unavailable"
    }

    override fun readAt(path: String, offset: Long, n: Long): ByteArray = guard { doReadAt(path, offset, n) }
    override fun fileSize(path: String): Long = guard { doFileSize(path) }
    override fun listDir(path: String): String = guard { doListDir(path) }
    override fun mkdir(path: String) = guardWrite { doMkdir(path) }
    override fun writeFile(path: String, data: ByteArray) = guardWrite { doWriteFile(path, data) }
    override fun openWriter(path: String): StorageWriter = guardWrite { doOpenWriter(path) }
    override fun remove(path: String) = guardWrite { doRemove(path) }
    override fun removeAll(path: String) = guardWrite { doRemoveAll(path) }
    override fun rename(from: String, to: String) = guardWrite { doRename(from, to) }

    private fun docUri(docId: String) = DocumentsContract.buildDocumentUriUsingTree(treeUri, docId)

    private fun notFound(path: String) = FileNotFoundException("not_found: $path")

    private fun split(path: String) = path.trim('/').split('/').filter { it.isNotEmpty() }

    /** Throws if the tree is unreachable (permission revoked, provider gone). */
    fun probe() = guard { children(rootId, fresh = true); Unit }

    private fun children(dirId: String, fresh: Boolean = false): Map<String, Child> {
        if (!fresh) synchronized(cache) { cache[dirId]?.let { return it } }
        val out = LinkedHashMap<String, Child>()
        val uri = DocumentsContract.buildChildDocumentsUriUsingTree(treeUri, dirId)
        val cols = arrayOf(Document.COLUMN_DOCUMENT_ID, Document.COLUMN_DISPLAY_NAME, Document.COLUMN_MIME_TYPE, Document.COLUMN_SIZE)
        val c = try {
            resolver.query(uri, cols, null, null, null)
        } catch (e: IllegalArgumentException) {
            throw FileNotFoundException("not_found: $dirId")
        } ?: throw IOException("provider returned no cursor for $dirId")
        c.use {
            while (it.moveToNext()) {
                val name = it.getString(1) ?: continue
                out[name] = Child(it.getString(0), name, it.getString(2) == Document.MIME_TYPE_DIR, if (it.isNull(3)) null else it.getLong(3))
            }
        }
        synchronized(cache) { cache[dirId] = out }
        return out
    }

    private fun invalidate(dirId: String) = synchronized(cache) { cache.remove(dirId); Unit }

    /** Resolves [path] to its entry; the root is a synthetic directory entry. Null if missing. */
    private fun lookup(path: String): Child? {
        var cur = Child(rootId, "", true, null)
        for (part in split(path)) {
            if (!cur.isDir) return null
            cur = children(cur.docId)[part] ?: return null
        }
        return cur
    }

    private fun require(path: String): Child = lookup(path) ?: throw notFound(path)

    private fun parentOf(path: String): Pair<Child, String> {
        val parts = split(path)
        if (parts.isEmpty()) throw IOException("invalid_path: $path")
        val parent = lookup(parts.dropLast(1).joinToString("/"))
        if (parent == null || !parent.isDir) throw notFound(path)
        return parent to parts.last()
    }

    private fun doReadAt(path: String, offset: Long, n: Long): ByteArray {
        val child = require(path)
        if (child.isDir) throw IOException("is a directory: $path")
        val pfd = try {
            resolver.openFileDescriptor(docUri(child.docId), "r")
        } catch (e: FileNotFoundException) {
            throw notFound(path)
        } ?: throw IOException("provider returned no descriptor for $path")
        pfd.use {
            FileInputStream(it.fileDescriptor).use { ins ->
                val buf = ByteBuffer.allocate(n.toInt())
                val ch = ins.channel
                while (buf.hasRemaining()) {
                    val r = ch.read(buf, offset + buf.position())
                    if (r <= 0) break
                }
                return buf.array().copyOf(buf.position())
            }
        }
    }

    private fun doFileSize(path: String): Long {
        val child = require(path)
        child.size?.let { return it }
        val pfd = resolver.openFileDescriptor(docUri(child.docId), "r") ?: throw notFound(path)
        return pfd.use { it.statSize }
    }

    private fun doListDir(path: String): String {
        val dir = require(path)
        if (!dir.isDir) throw IOException("not a directory: $path")
        val arr = JSONArray()
        for (c in children(dir.docId, fresh = true).values) {
            arr.put(JSONObject().put("name", c.name).put("isDir", c.isDir).put("size", c.size ?: 0L))
        }
        return arr.toString()
    }

    /**
     * Boolean across gomobile cannot carry an exception, so a revoked tree is
     * recorded in [revoked] and reported by the next call that can throw.
     */
    override fun exists(path: String): Boolean = try {
        !revoked && lookup(path) != null
    } catch (e: SecurityException) {
        revoked = true
        false
    } catch (e: Exception) {
        false
    }

    @Synchronized
    private fun doMkdir(path: String) {
        val (parent, name) = parentOf(path)
        if (children(parent.docId, fresh = true).containsKey(name)) throw IOException("exists: $path")
        try {
            DocumentsContract.createDocument(resolver, docUri(parent.docId), Document.MIME_TYPE_DIR, name)
                ?: throw IOException("provider could not create directory $path")
        } finally {
            invalidate(parent.docId)
        }
    }

    @Synchronized
    private fun doWriteFile(path: String, data: ByteArray) {
        val (parent, name) = parentOf(path)
        val existing = children(parent.docId, fresh = true)[name]
        if (existing != null) {
            if (existing.isDir) throw IOException("is a directory: $path")
            try {
                resolver.openOutputStream(docUri(existing.docId), "wt")!!.use { it.write(data) }
            } finally {
                invalidate(parent.docId)
            }
            return
        }
        val w = newWriter(parent, name)
        try {
            w.write(data)
            w.close()
        } catch (e: Exception) {
            w.abort()
            throw e
        }
    }

    @Synchronized
    private fun doOpenWriter(path: String): StorageWriter {
        val (parent, name) = parentOf(path)
        return newWriter(parent, name)
    }

    private fun newWriter(parent: Child, name: String): Writer {
        if (children(parent.docId, fresh = true).containsKey(name)) throw IOException("exists: $name")
        val uri = DocumentsContract.createDocument(resolver, docUri(parent.docId), "application/octet-stream", name)
            ?: throw IOException("provider could not create $name")
        invalidate(parent.docId)
        val out = try {
            try {
                resolver.openOutputStream(uri, "w")
            } catch (e: Exception) {
                resolver.openOutputStream(uri, "wt")
            } ?: throw IOException("provider returned no stream for $name")
        } catch (e: Exception) {
            deleteQuietly(uri)
            throw e
        }
        return Writer(uri, parent.docId, out)
    }

    private fun deleteQuietly(uri: Uri) {
        try {
            DocumentsContract.deleteDocument(resolver, uri)
        } catch (_: Exception) {
        }
    }

    /** Streams into a new document. Any failure (or [abort]) removes the document. */
    inner class Writer(private val uri: Uri, private val parentId: String, private val out: OutputStream) : StorageWriter {
        private var failed = false
        private var done = false

        override fun write(p: ByteArray) {
            try {
                out.write(p)
            } catch (e: Exception) {
                failed = true
                throw if (e is SecurityException) readOnly(e) else e
            }
        }

        override fun close() {
            if (done) return
            done = true
            var err: Exception? = null
            try {
                out.close()
            } catch (e: Exception) {
                err = e
            }
            invalidate(parentId)
            if (err != null || failed) {
                deleteQuietly(uri)
                throw (if (err is SecurityException) readOnly(err) else err) ?: IOException("write failed; partial file removed")
            }
        }

        fun abort() {
            if (done) return
            done = true
            try {
                out.close()
            } catch (_: Exception) {
            }
            deleteQuietly(uri)
            invalidate(parentId)
        }
    }

    @Synchronized
    private fun doRemove(path: String) {
        val c = require(path)
        if (c.docId == rootId) throw IOException("invalid_path: $path")
        if (c.isDir && children(c.docId, fresh = true).isNotEmpty()) throw IOException("directory not empty: $path")
        delete(path, c)
    }

    @Synchronized
    private fun doRemoveAll(path: String) {
        val c = lookup(path) ?: return
        if (c.docId == rootId) throw IOException("invalid_path: $path")
        delete(path, c)
    }

    private fun delete(path: String, c: Child) {
        val (parent, _) = parentOf(path)
        try {
            if (!DocumentsContract.deleteDocument(resolver, docUri(c.docId))) throw IOException("could not delete $path")
        } finally {
            invalidate(parent.docId)
            invalidate(c.docId)
        }
    }

    @Synchronized
    private fun doRename(from: String, to: String) {
        val src = require(from)
        val (srcParent, srcName) = parentOf(from)
        val (dstParent, dstName) = parentOf(to)
        if (children(dstParent.docId, fresh = true).containsKey(dstName)) throw IOException("exists: $to")
        try {
            if (srcParent.docId == dstParent.docId) {
                if (srcName != dstName) renameDoc(src.docId, dstName)
                return
            }
            val moved = try {
                DocumentsContract.moveDocument(resolver, docUri(src.docId), docUri(srcParent.docId), docUri(dstParent.docId))
            } catch (e: UnsupportedOperationException) {
                null
            } catch (e: IllegalStateException) {
                null
            }
            if (moved == null) {
                copyTree(src, dstParent, dstName)
                if (!DocumentsContract.deleteDocument(resolver, docUri(src.docId))) throw IOException("could not delete $from after copy")
            } else if (srcName != dstName) {
                renameUri(moved, dstName)
            }
        } finally {
            invalidate(srcParent.docId)
            invalidate(dstParent.docId)
            invalidate(src.docId)
        }
    }

    private fun renameDoc(docId: String, name: String) = renameUri(docUri(docId), name)

    private fun renameUri(uri: Uri, name: String) {
        DocumentsContract.renameDocument(resolver, uri, name) ?: throw IOException("provider could not rename to $name")
    }

    /** Fallback for providers without move: copy bytes (still ciphertext) then the caller deletes the source. */
    private fun copyTree(src: Child, dstParent: Child, dstName: String) {
        if (src.isDir) {
            val dir = DocumentsContract.createDocument(resolver, docUri(dstParent.docId), Document.MIME_TYPE_DIR, dstName)
                ?: throw IOException("provider could not create directory $dstName")
            val dirId = DocumentsContract.getDocumentId(dir)
            val made = Child(dirId, dstName, true, null)
            try {
                for (c in children(src.docId, fresh = true).values) copyTree(c, made, c.name)
            } catch (e: Exception) {
                deleteQuietly(dir)
                throw e
            }
        } else {
            val w = newWriter(dstParent, dstName)
            try {
                val pfd = resolver.openFileDescriptor(docUri(src.docId), "r") ?: throw IOException("cannot open source")
                pfd.use { FileInputStream(it.fileDescriptor).use { ins -> ins.copyTo(OutputStreamAdapter(w)) } }
                w.close()
            } catch (e: Exception) {
                w.abort()
                throw e
            }
        }
    }

    private class OutputStreamAdapter(private val w: Writer) : OutputStream() {
        override fun write(b: Int) = w.write(byteArrayOf(b.toByte()))
        override fun write(b: ByteArray, off: Int, len: Int) = w.write(b.copyOfRange(off, off + len))
    }
}
