import React from 'react';
import { useNostrReleases } from './NostrReleaseProvider';
import styled from 'styled-components';
import PageContainer from './common/PageContainer';
import Button from './common/Button';
import { getReleaseVersion } from '../utils/releaseUtils';

interface RouterInfo {
  ip: string;
  boardName?: string;
  architecture?: string;
}

interface UpdateInfo {
  latest: string;
  installed: string;
  canUpgrade: boolean;
  nip94Event?: any;
}

interface CompleteProps {
  router: RouterInfo | null;
  onFlashNext: () => void;
}

const InfoGrid = styled.div`
  display: grid;
  gap: ${props => props.theme.space.md};
  margin-bottom: ${props => props.theme.space.xl};
`;

const InfoItem = styled.div`
  display: flex;
  justify-content: space-between;
  align-items: center;
`;

const InfoLabel = styled.span`
  font-weight: ${props => props.theme.fontWeights.medium};
`;

const InfoValue = styled.span`
  color: ${props => props.theme.colors.textSecondary};
`;

const UpdateStatus = styled.div<{ hasUpdate: boolean }>`
  display: flex;
  align-items: center;
  padding: 0.75rem 1rem;
  border-radius: ${props => props.theme.radii.md};
  margin-bottom: 1.5rem;
  background-color: ${props => 
    props.hasUpdate 
      ? `${props.theme.colors.primaryLight}`
      : `${props.theme.colors.backgroundAlt}`
  };
  color: ${props => 
    props.hasUpdate 
      ? `${props.theme.colors.primary}`
      : `${props.theme.colors.textSecondary}`
  };
`;

const UpdateStatusIcon = styled.span`
  margin-right: 0.5rem;
  font-weight: bold;
`;

const ActionButton = styled(Button)`
  width: 100%;
  margin-top: 1.5rem;
`;

const FlashNextContainer = styled.div`
  text-align: center;
  padding: 1.5rem;
  border: 1px dashed ${props => props.theme.colors.border};
  border-radius: ${props => props.theme.radii.md};
  margin-top: 2rem;
`;

const FlashNextTitle = styled.h3`
  font-size: ${props => props.theme.fontSizes.lg};
  color: ${props => props.theme.colors.text};
  margin-bottom: 0.5rem;
`;

const FlashNextDescription = styled.p`
  color: ${props => props.theme.colors.textSecondary};
  margin-bottom: 1.5rem;
`;

const Complete: React.FC<CompleteProps> = ({
  router,
  onFlashNext
}) => {
  const { releases, loading, error } = useNostrReleases();
  
  // Create updateInfo from releases data
  const updateInfo = releases.length > 0 ? {
    latest: getReleaseVersion(releases[0]) || 'unknown',
    installed: 'v0.0.0', // Placeholder for installed version
    canUpgrade: true, // Simplified logic, assumes newer version is available
  } : null;
  
  const hasUpdate = updateInfo?.canUpgrade || false;

  return (
    <PageContainer 
      title="Installation Complete" 
      subtitle="Your router is now running TollGateOS. It's ready to use."
    >
      {router && (
        <InfoGrid>
          <InfoItem>
            <InfoLabel>Router IP:</InfoLabel>
            <InfoValue>{router.ip}</InfoValue>
          </InfoItem>
          {router.boardName && (
            <InfoItem>
              <InfoLabel>Model:</InfoLabel>
              <InfoValue>{router.boardName}</InfoValue>
            </InfoItem>
          )}
          {router.architecture && (
            <InfoItem>
              <InfoLabel>Architecture:</InfoLabel>
              <InfoValue>{router.architecture}</InfoValue>
            </InfoItem>
          )}
        </InfoGrid>
      )}
      
      {loading ? (
        <InfoGrid>
          <InfoItem>
            <InfoLabel>Status:</InfoLabel>
            <InfoValue>Loading version information...</InfoValue>
          </InfoItem>
        </InfoGrid>
      ) : error ? (
        <InfoGrid>
          <InfoItem>
            <InfoLabel>Error:</InfoLabel>
            <InfoValue>{error}</InfoValue>
          </InfoItem>
        </InfoGrid>
      ) : updateInfo && (
        <>
          <InfoGrid>
            <InfoItem>
              <InfoLabel>Installed Version:</InfoLabel>
              <InfoValue>{updateInfo.installed}</InfoValue>
            </InfoItem>
            <InfoItem>
              <InfoLabel>Latest Version:</InfoLabel>
              <InfoValue>{updateInfo.latest}</InfoValue>
            </InfoItem>
          </InfoGrid>
          
          <UpdateStatus hasUpdate={hasUpdate}>
            <UpdateStatusIcon>
              {hasUpdate ? '!' : '✓'}
            </UpdateStatusIcon>
            {hasUpdate 
              ? 'A new version of TollGateOS is available!' 
              : 'Your router is running the latest version of TollGateOS'}
          </UpdateStatus>
        </>
      )}
      
      <FlashNextContainer>
        <FlashNextTitle>Want to flash another router?</FlashNextTitle>
        <FlashNextDescription>
          You can quickly install TollGateOS on another router by clicking the button below.
        </FlashNextDescription>
        
        <ActionButton 
          variant="primary" 
          size="large" 
          onClick={onFlashNext}
        >
          Flash Another Router
        </ActionButton>
      </FlashNextContainer>
    </PageContainer>
  );
};

export default Complete;