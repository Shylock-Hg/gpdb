/*-------------------------------------------------------------------------
 *
 * cdbdtxcontextinfo.c
 *
 * Portions Copyright (c) 2007-2008, Greenplum inc
 * Portions Copyright (c) 2012-Present VMware, Inc. or its affiliates.
 *
 *
 * IDENTIFICATION
 *	    src/backend/cdb/cdbdtxcontextinfo.c
 *
 *-------------------------------------------------------------------------
 */
#include "postgres.h"

#include "cdb/cdbdistributedsnapshot.h"
#include "cdb/cdblocaldistribxact.h"
#include "cdb/cdbdtxcontextinfo.h"
#include "miscadmin.h"
#include "access/transam.h"
#include "cdb/cdbvars.h"
#include "cdb/cdbtm.h"
#include "access/xact.h"
#include "utils/guc.h"
#include "utils/session_state.h"

/*
 * process local cache used to identify "dispatch units"
 *
 * Note, this is only required because the dispatcher emits multiple statements (which will
 * correspond to multiple local-xids on the segments) under the same distributed-xid.
 *
 */
static uint32 syncCount = 1;

void
DtxContextInfo_CreateOnCoordinator(DtxContextInfo *dtxContextInfo, bool inCursor,
							  int txnOptions, Snapshot snapshot)
{
	int			i;
	CommandId	curcid = 0;

	if (snapshot)
		curcid = snapshot->curcid;

	DtxContextInfo_Reset(dtxContextInfo);

	dtxContextInfo->distributedXid = getDistributedTransactionId();
	if (dtxContextInfo->distributedXid != InvalidDistributedTransactionId)
		dtxContextInfo->curcid = curcid;

	/*
	 * When this is an extended query, all the dispatchs will go to
	 * the reader gangs, don't increase 'syncCount' so that all the
	 * dispatch could share the same snapshot created by 'gp_write_shared_snapshot'.
	 */
	dtxContextInfo->segmateSync = inCursor ? syncCount : ++syncCount;
	if (dtxContextInfo->segmateSync == (~(uint32)0))
		ereport(FATAL,
				(errcode(ERRCODE_PROGRAM_LIMIT_EXCEEDED),
				 errmsg("cannot have more than 2^32-2 commands in a session")));

	AssertImply(inCursor && !IS_HOT_STANDBY_QD(),
				dtxContextInfo->distributedXid != InvalidDistributedTransactionId &&
				gp_command_count == MySessionState->latestCursorCommandId);

	dtxContextInfo->cursorContext = inCursor;
	dtxContextInfo->nestingLevel = GetCurrentTransactionNestLevel();

	elog((Debug_print_full_dtm ? LOG : DEBUG5),
		 "DtxContextInfo_CreateOnCoordinator: created dtxcontext with dxid "UINT64_FORMAT" nestingLevel %d segmateSync %u/%u (current/cached)",
		 dtxContextInfo->distributedXid, dtxContextInfo->nestingLevel,
		 dtxContextInfo->segmateSync, syncCount);

	/* Pass the snapshot mode and the info needed by each mode. */
	if (snapshot)
	{
		dtxContextInfo->gpSnapshotMode = snapshot->gpSnapshotMode;
		if (snapshot->gpSnapshotMode == GP_SNAPSHOT_MODE_DISTRIBUTED)
			DistributedSnapshot_Copy(&dtxContextInfo->gpSnapshotInfo.distributedSnapshot,
									 &snapshot->gpSnapshotInfo.distribSnapshotWithLocalMapping.ds);
		else if (snapshot->gpSnapshotMode == GP_SNAPSHOT_MODE_RESTOREPOINT)
			StrNCpy(dtxContextInfo->gpSnapshotInfo.rpname, snapshot->gpSnapshotInfo.rpname, MAXFNAMELEN);
	}
	else
		dtxContextInfo->gpSnapshotMode = GP_SNAPSHOT_MODE_LOCAL;

	dtxContextInfo->distributedTxnOptions = txnOptions;

	if (DEBUG5 >= log_min_messages || Debug_print_full_dtm)
	{
		char		gid[TMGIDSIZE];
		DistributedSnapshot *ds = &dtxContextInfo->gpSnapshotInfo.distributedSnapshot;

		if (!getDistributedTransactionIdentifier(gid))
			memcpy(gid, "<empty>", 8);

		elog((Debug_print_full_dtm ? LOG : DEBUG5),
			 "DtxContextInfo_CreateOnCoordinator Gp_role is DISPATCH and have gid = %s --> have distributed snapshot", gid);
		if (dtxContextInfo->gpSnapshotMode == GP_SNAPSHOT_MODE_DISTRIBUTED)
		{
			elog((Debug_print_full_dtm ? LOG : DEBUG5),
				 "DtxContextInfo_CreateOnCoordinator distributedXid = "UINT64_FORMAT", "
				 "distributedSnapshotHeader (xminAllDistributedSnapshots "UINT64_FORMAT", xmin = "UINT64_FORMAT", xmax = "UINT64_FORMAT", count = %d)",
				 dtxContextInfo->distributedXid,
				 ds->xminAllDistributedSnapshots,
				 ds->xmin,
				 ds->xmax,
				 ds->count);

			for (i = 0; i < ds->count; i++)
			{
				elog((Debug_print_full_dtm ? LOG : DEBUG5),
					 "....    distributedSnapshotData->xip[%d] = "UINT64_FORMAT,
					 i, ds->inProgressXidArray[i]);
			}
		}
		else if (dtxContextInfo->gpSnapshotMode == GP_SNAPSHOT_MODE_RESTOREPOINT)
		{
			elog((Debug_print_full_dtm ? LOG : DEBUG5),
				"DtxContextInfo_CreateOnCoordinator restore point name: %s", dtxContextInfo->gpSnapshotInfo.rpname);
		}
		elog((Debug_print_full_dtm ? LOG : DEBUG5),
			 "DtxContextInfo_CreateOnCoordinator curcid = %u",
			 dtxContextInfo->curcid);

		elog((Debug_print_full_dtm ? LOG : DEBUG5),
			 "DtxContextInfo_CreateOnCoordinator txnOptions = 0x%x, needDtx = %s, explicitBegin = %s, isoLevel = %s, readOnly = %s.",
			 txnOptions,
			 (isMppTxOptions_NeedDtx(txnOptions) ? "true" : "false"),
			 (isMppTxOptions_ExplicitBegin(txnOptions) ? "true" : "false"),
			 IsoLevelAsUpperString(mppTxOptions_IsoLevel(txnOptions)),
			 (isMppTxOptions_ReadOnly(txnOptions) ? "true" : "false"));
	}
}

int
DtxContextInfo_SerializeSize(DtxContextInfo *dtxContextInfo)
{
	int			size = 0;

	size += sizeof(DistributedTransactionId);	/* distributedXid */

	if (dtxContextInfo->distributedXid != InvalidDistributedTransactionId)
	{
		size += TMGIDSIZE;		/* distributedId */
		size += sizeof(CommandId);	/* curcid */
	}

	size += sizeof(uint32);		/* segmateSync */
	size += sizeof(uint32);		/* nestingLevel */
	size += sizeof(GpSnapshotMode);		/* gpSnapshotMode */
	size += sizeof(bool);		/* cursorContext */

	if (dtxContextInfo->gpSnapshotMode == GP_SNAPSHOT_MODE_DISTRIBUTED)
	{
		size += DistributedSnapshot_SerializeSize(
												  &dtxContextInfo->gpSnapshotInfo.distributedSnapshot);
	}
	else if (dtxContextInfo->gpSnapshotMode == GP_SNAPSHOT_MODE_RESTOREPOINT)
		size += MAXFNAMELEN;

	size += sizeof(int);		/* distributedTxnOptions */

	elog((Debug_print_full_dtm ? LOG : DEBUG5),
		 "DtxContextInfo_SerializeSize is returning size = %d", size);

	return size;
}

void
DtxContextInfo_Serialize(char *buffer, DtxContextInfo *dtxContextInfo)
{
	char	   *p = buffer;
	int			i;
	int			used;
	DistributedSnapshot *ds = &dtxContextInfo->gpSnapshotInfo.distributedSnapshot;

	memcpy(p, &dtxContextInfo->distributedXid, sizeof(DistributedTransactionId));
	p += sizeof(DistributedTransactionId);
	if (dtxContextInfo->distributedXid != InvalidDistributedTransactionId)
	{
		memcpy(p, &dtxContextInfo->curcid, sizeof(CommandId));
		p += sizeof(CommandId);
	}
	else
	{
		elog((Debug_print_full_dtm ? LOG : DEBUG5),
			 "DtxContextInfo_Serialize only copied InvalidDistributedTransactionId");
	}

	elog((Debug_print_full_dtm ? LOG : DEBUG3),
		 "DtxContextInfo_Serialize distributedXid = "UINT64_FORMAT", curcid %d nestingLevel %d segmateSync %u",
		 dtxContextInfo->distributedXid,
		 dtxContextInfo->curcid, dtxContextInfo->nestingLevel, dtxContextInfo->segmateSync);

	memcpy(p, &dtxContextInfo->segmateSync, sizeof(uint32));
	p += sizeof(uint32);

	memcpy(p, &dtxContextInfo->nestingLevel, sizeof(uint32));
	p += sizeof(uint32);

	memcpy(p, &dtxContextInfo->gpSnapshotMode, sizeof(GpSnapshotMode));
	p += sizeof(GpSnapshotMode);

	memcpy(p, &dtxContextInfo->cursorContext, sizeof(bool));
	p += sizeof(bool);

	if (dtxContextInfo->gpSnapshotMode == GP_SNAPSHOT_MODE_DISTRIBUTED)
	{
		p += DistributedSnapshot_Serialize(ds, p);
	}
	else if (dtxContextInfo->gpSnapshotMode == GP_SNAPSHOT_MODE_RESTOREPOINT)
	{
		memcpy(p, dtxContextInfo->gpSnapshotInfo.rpname, MAXFNAMELEN);
		p += MAXFNAMELEN;
	}

	memcpy(p, &dtxContextInfo->distributedTxnOptions, sizeof(int));
	p += sizeof(int);

	used = (p - buffer);

	if (DEBUG5 >= log_min_messages || Debug_print_full_dtm || Debug_print_snapshot_dtm)
	{
		elog((Debug_print_full_dtm ? LOG : DEBUG5),
			 "DtxContextInfo_Serialize distributedXid = "UINT64_FORMAT", "
			 "curcid %d",
			 dtxContextInfo->distributedXid,
			 dtxContextInfo->curcid);

		if (dtxContextInfo->gpSnapshotMode == GP_SNAPSHOT_MODE_DISTRIBUTED)
		{
			elog((Debug_print_full_dtm ? LOG : DEBUG5),
				 "distributedSnapshotHeader (xminAllDistributedSnapshots "UINT64_FORMAT", xmin = "UINT64_FORMAT", xmax = "UINT64_FORMAT", count = %d)",
				 ds->xminAllDistributedSnapshots,
				 ds->xmin,
				 ds->xmax,
				 ds->count);
			for (i = 0; i < ds->count; i++)
			{
				elog((Debug_print_full_dtm ? LOG : DEBUG5),
					 "....    inProgressXidArray[%d] = "UINT64_FORMAT,
					 i, ds->inProgressXidArray[i]);
			}
			elog((Debug_print_snapshot_dtm ? LOG : DEBUG5),
				 "[Distributed Snapshot #%u] *Serialize* currcid = %d (gxid = "UINT64_FORMAT", '%s')",
				 ds->distribSnapshotId,
				 dtxContextInfo->curcid,
				 getDistributedTransactionId(),
				 DtxContextToString(DistributedTransactionContext));
		}
		else if (dtxContextInfo->gpSnapshotMode == GP_SNAPSHOT_MODE_RESTOREPOINT)
			elog((Debug_print_snapshot_dtm ? LOG : DEBUG5),
				 "[Restore Point Snapshot %s]", dtxContextInfo->gpSnapshotInfo.rpname);

		elog((Debug_print_full_dtm ? LOG : DEBUG5), "DtxContextInfo_Serialize txnOptions = 0x%x", dtxContextInfo->distributedTxnOptions);
		elog((Debug_print_full_dtm ? LOG : DEBUG5), "DtxContextInfo_Serialize copied %d bytes", used);
	}
}

void
DtxContextInfo_Reset(DtxContextInfo *dtxContextInfo)
{
	dtxContextInfo->distributedXid = InvalidDistributedTransactionId;

	dtxContextInfo->curcid = 0;
	dtxContextInfo->segmateSync = 0;
	dtxContextInfo->nestingLevel = 0;

	/*
	 * Perform reset on the GpSnapshotInfo depending on specific GpSnapshotMode.
	 * One might wonder why we cannot just reset everything to 0: we can't 
	 * because the distributed snapshot might contains an already-allocated 
	 * inProgressXidArray, and it will re-use its space forever.
	 */
	if (dtxContextInfo->gpSnapshotMode == GP_SNAPSHOT_MODE_DISTRIBUTED)
		DistributedSnapshot_Reset(&dtxContextInfo->gpSnapshotInfo.distributedSnapshot);
	else if (dtxContextInfo->gpSnapshotMode == GP_SNAPSHOT_MODE_RESTOREPOINT)
		MemSet(dtxContextInfo->gpSnapshotInfo.rpname, 0, MAXFNAMELEN);

	/* Reset the mode. We will set it again when needed. */
	dtxContextInfo->gpSnapshotMode = GP_SNAPSHOT_MODE_LOCAL;

	dtxContextInfo->distributedTxnOptions = 0;
}

void
DtxContextInfo_Copy(
					DtxContextInfo *target,
					DtxContextInfo *source)
{
	DtxContextInfo_Reset(target);

	target->distributedXid = source->distributedXid;
	target->segmateSync = source->segmateSync;
	target->nestingLevel = source->nestingLevel;

	target->curcid = source->curcid;

	target->gpSnapshotMode = source->gpSnapshotMode;
	target->cursorContext = source->cursorContext;

	if (source->gpSnapshotMode == GP_SNAPSHOT_MODE_DISTRIBUTED)
		DistributedSnapshot_Copy(&target->gpSnapshotInfo.distributedSnapshot,
								 &source->gpSnapshotInfo.distributedSnapshot);
	else if (source->gpSnapshotMode == GP_SNAPSHOT_MODE_RESTOREPOINT)
		StrNCpy(target->gpSnapshotInfo.rpname, source->gpSnapshotInfo.rpname, MAXFNAMELEN);

	target->distributedTxnOptions = source->distributedTxnOptions;

	elog((Debug_print_full_dtm ? LOG : DEBUG5),
		 "DtxContextInfo_Copy distributed {xid "UINT64_FORMAT"}, "
		 "command id %d",
		 target->distributedXid,
		 target->curcid);

	if (target->gpSnapshotMode == GP_SNAPSHOT_MODE_DISTRIBUTED)
		elog((Debug_print_full_dtm ? LOG : DEBUG5),
			 "distributed snapshot {xminAllDistributedSnapshots "UINT64_FORMAT", snapshot id %d, "
			 "xmin "UINT64_FORMAT", count %d, xmax "UINT64_FORMAT"}",
			 target->gpSnapshotInfo.distributedSnapshot.xminAllDistributedSnapshots,
			 target->gpSnapshotInfo.distributedSnapshot.distribSnapshotId,
			 target->gpSnapshotInfo.distributedSnapshot.xmin,
			 target->gpSnapshotInfo.distributedSnapshot.count,
			 target->gpSnapshotInfo.distributedSnapshot.xmax);
	else if (target->gpSnapshotMode == GP_SNAPSHOT_MODE_RESTOREPOINT)
		elog((Debug_print_full_dtm ? LOG : DEBUG5),
			"restore point name: %s", target->gpSnapshotInfo.rpname);

}

void
DtxContextInfo_Deserialize(const char *serializedDtxContextInfo,
						   int serializedDtxContextInfolen,
						   DtxContextInfo *dtxContextInfo)
{
	int			i;
	DistributedSnapshot *ds = &dtxContextInfo->gpSnapshotInfo.distributedSnapshot;

	DtxContextInfo_Reset(dtxContextInfo);

	if (serializedDtxContextInfolen > 0)
	{
		const char *p = serializedDtxContextInfo;

		elog((Debug_print_full_dtm ? LOG : DEBUG5),
			 "DtxContextInfo_Deserialize serializedDtxContextInfolen = %d.",
			 serializedDtxContextInfolen);

		memcpy(&dtxContextInfo->distributedXid, p, sizeof(DistributedTransactionId));
		p += sizeof(DistributedTransactionId);

		if (dtxContextInfo->distributedXid != InvalidDistributedTransactionId)
		{
			memcpy(&dtxContextInfo->curcid, p, sizeof(CommandId));
			p += sizeof(CommandId);
		}
		else
		{
			elog((Debug_print_full_dtm ? LOG : DEBUG5),
				 "DtxContextInfo_Deserialize distributedXid was InvalidDistributedTransactionId");
		}

		memcpy(&dtxContextInfo->segmateSync, p, sizeof(uint32));
		p += sizeof(uint32);
		memcpy(&dtxContextInfo->nestingLevel, p, sizeof(uint32));
		p += sizeof(uint32);
		memcpy(&dtxContextInfo->gpSnapshotMode, p, sizeof(GpSnapshotMode));
		p += sizeof(GpSnapshotMode);

		memcpy(&dtxContextInfo->cursorContext, p, sizeof(bool));
		p += sizeof(bool);

		elog((Debug_print_full_dtm ? LOG : DEBUG3),
			 "DtxContextInfo_Deserialize distributedXid = "UINT64_FORMAT", curcid %d nestingLevel %d segmateSync %u as %s",
			 dtxContextInfo->distributedXid,
			 dtxContextInfo->curcid, dtxContextInfo->nestingLevel,
			 dtxContextInfo->segmateSync, (Gp_is_writer ? "WRITER" : "READER"));

		if (dtxContextInfo->gpSnapshotMode == GP_SNAPSHOT_MODE_DISTRIBUTED)
		{
			p += DistributedSnapshot_Deserialize(p, ds);
		}
		else if (dtxContextInfo->gpSnapshotMode == GP_SNAPSHOT_MODE_RESTOREPOINT)
		{
			memcpy(dtxContextInfo->gpSnapshotInfo.rpname, p, MAXFNAMELEN);
			p += MAXFNAMELEN;
		}
		else
		{
			elog((Debug_print_full_dtm ? LOG : DEBUG5),
				 "DtxContextInfo_Deserialize no distributed snapshot");
		}

		memcpy(&dtxContextInfo->distributedTxnOptions, p, sizeof(int));
		p += sizeof(int);

		if (DEBUG5 >= log_min_messages || Debug_print_full_dtm)
		{
			elog((Debug_print_full_dtm ? LOG : DEBUG5),
				 "DtxContextInfo_Deserialize distributedXid = "UINT64_FORMAT,
				 dtxContextInfo->distributedXid);

			if (dtxContextInfo->gpSnapshotMode == GP_SNAPSHOT_MODE_DISTRIBUTED)
			{
				elog((Debug_print_full_dtm ? LOG : DEBUG5),
					 "distributedSnapshotHeader (xminAllDistributedSnapshots "UINT64_FORMAT", xmin = "UINT64_FORMAT", xmax = "UINT64_FORMAT", count = %d)",
					 ds->xminAllDistributedSnapshots,
					 ds->xmin,
					 ds->xmax,
					 ds->count);

				for (i = 0; i < ds->count; i++)
				{
					elog((Debug_print_full_dtm ? LOG : DEBUG5),
						 "....    inProgressXidArray[%d] = "UINT64_FORMAT,
						 i, ds->inProgressXidArray[i]);
				}

				elog((Debug_print_snapshot_dtm ? LOG : DEBUG5),
					 "[Distributed Snapshot #%u] *Deserialize* currcid = %d (gxid = "UINT64_FORMAT", '%s')",
					 ds->distribSnapshotId,
					 dtxContextInfo->curcid,
					 getDistributedTransactionId(),
					 DtxContextToString(DistributedTransactionContext));
			}
			else if (dtxContextInfo->gpSnapshotMode == GP_SNAPSHOT_MODE_RESTOREPOINT)
				elog((Debug_print_snapshot_dtm ? LOG : DEBUG5),
					"restore point name %s", dtxContextInfo->gpSnapshotInfo.rpname);

			elog((Debug_print_full_dtm ? LOG : DEBUG5),
				 "DtxContextInfo_Deserialize txnOptions = 0x%x",
				 dtxContextInfo->distributedTxnOptions);
		}
	}
	else
	{
		Assert(dtxContextInfo->distributedXid == InvalidDistributedTransactionId);
		Assert(dtxContextInfo->distributedTxnOptions == 0);
	}
}
