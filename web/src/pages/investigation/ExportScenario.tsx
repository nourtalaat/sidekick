import { FileOutput } from "lucide-react";
import { useNavigate } from "react-router";
import { useExportScenario } from "../../api/scenarios";
import { Button } from "../../components/Button";

/**
 * Exports a confirmed investigation as a benchmark scenario draft and opens
 * the draft on the review queue. Asking again opens the draft the
 * investigation has already.
 */
export function ExportScenario({ investigationId }: { investigationId: string }) {
  const navigate = useNavigate();
  const exportScenario = useExportScenario(investigationId);

  return (
    <>
      <Button
        onClick={() =>
          exportScenario.mutate(undefined, {
            onSuccess: ({ scenario }) => void navigate(`/review?tab=scenarios&id=${encodeURIComponent(scenario.id)}`),
          })
        }
        disabled={exportScenario.isPending}
      >
        <FileOutput size={16} aria-hidden="true" />
        {exportScenario.isPending ? "Exporting" : "Export as scenario"}
      </Button>
      {exportScenario.isError && (
        <p role="alert" className="sk-field__error">
          {exportScenario.error.message}
        </p>
      )}
    </>
  );
}
