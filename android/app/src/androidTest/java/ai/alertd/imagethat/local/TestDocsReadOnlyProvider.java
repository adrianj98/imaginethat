package ai.alertd.imagethat.local;

/** A provider whose grant allows reads but denies every write, with a seeded "seed.bin". */
public class TestDocsReadOnlyProvider extends TestDocsProvider {
    public static final String AUTHORITY_RO = "ai.alertd.imagethat.test.docs.ro";

    @Override
    protected boolean denyWrites() {
        return true;
    }

    @Override
    protected String authority() {
        return AUTHORITY_RO;
    }
}
